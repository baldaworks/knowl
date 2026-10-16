package postgres

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/pressly/goose/v3/lock"
)

//go:embed pgvector_migrations/*.sql
var pgvectorMigrationFiles embed.FS

const pgvectorHistory = "goose_pgvector_db_version"

// Identifiers are resolved from extension-owned catalog objects, never options
// or the ambient search path. Keep raw driver errors out of this cached state.
type nativeVectorCapability struct {
	schema  string
	typeOID int64
	dimsOID int64
}

func configureNativeVectors(ctx context.Context, db *sql.DB, locker lock.SessionLocker) (nativeVectorCapability, error) {
	var capability nativeVectorCapability
	err := db.QueryRowContext(ctx, `SELECT n.nspname,t.oid::bigint,f.oid::bigint
        FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace
        JOIN pg_type t ON t.typnamespace=n.oid AND t.typname='vector'
        JOIN pg_depend td ON td.classid='pg_type'::regclass AND td.objid=t.oid AND td.refclassid='pg_extension'::regclass AND td.refobjid=e.oid AND td.deptype='e'
        JOIN pg_proc f ON f.pronamespace=n.oid AND f.proname='vector_dims' AND f.proargtypes=ARRAY[t.oid]::oidvector AND f.prorettype='int4'::regtype
        JOIN pg_depend fd ON fd.classid='pg_proc'::regclass AND fd.objid=f.oid AND fd.refclassid='pg_extension'::regclass AND fd.refobjid=e.oid AND fd.deptype='e'
        WHERE e.extname='vector' AND has_schema_privilege(n.oid,'USAGE') AND has_function_privilege(f.oid,'EXECUTE')
        AND EXISTS (SELECT 1 FROM pg_operator o JOIN pg_depend od ON od.classid='pg_operator'::regclass AND od.objid=o.oid AND od.refclassid='pg_extension'::regclass AND od.refobjid=e.oid AND od.deptype='e' WHERE o.oprnamespace=n.oid AND o.oprname='<=>' AND o.oprleft=t.oid AND o.oprright=t.oid AND o.oprresult='float8'::regtype)
        AND EXISTS (SELECT 1 FROM pg_proc nf JOIN pg_depend nd ON nd.classid='pg_proc'::regclass AND nd.objid=nf.oid AND nd.refclassid='pg_extension'::regclass AND nd.refobjid=e.oid AND nd.deptype='e' WHERE nf.pronamespace=n.oid AND nf.proname='vector_norm' AND nf.proargtypes=ARRAY[t.oid]::oidvector AND has_function_privilege(nf.oid,'EXECUTE'))`).Scan(&capability.schema, &capability.typeOID, &capability.dimsOID)
	if err != nil {
		return nativeVectorCapability{}, nativeUnavailable(ctx)
	}
	directory, err := fs.Sub(pgvectorMigrationFiles, "pgvector_migrations")
	if err != nil {
		return nativeVectorCapability{}, nativeUnavailable(ctx)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, directory, goose.WithTableName(pgvectorHistory), goose.WithSessionLocker(locker), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return nativeVectorCapability{}, nativeUnavailable(ctx)
	}
	if _, err := provider.Up(ctx); err != nil {
		return nativeVectorCapability{}, nativeUnavailable(ctx)
	}
	if err := validateNativeSchema(ctx, db, provider, capability); err != nil {
		return nativeVectorCapability{}, nativeUnavailable(ctx)
	}
	return capability, nil
}

func nativeUnavailable(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return embeddingFailure(knowl.RetrievalUnavailable)
}

func validateNativeSchema(ctx context.Context, db *sql.DB, provider *goose.Provider, capability nativeVectorCapability) error {
	// Use Goose's own store and source metadata to reject unrecognized history,
	// including unknown versions which Status alone does not enumerate.
	history, err := database.NewStore(database.DialectPostgres, pgvectorHistory)
	if err != nil {
		return err
	}
	versions, err := history.ListMigrations(ctx, db)
	if err != nil {
		return err
	}
	known := map[int64]bool{0: true}
	for _, source := range provider.ListSources() {
		known[source.Version] = true
	}
	seen := make(map[int64]bool, len(versions))
	for _, version := range versions {
		if !known[version.Version] || !version.IsApplied || seen[version.Version] {
			return nativeUnavailable(ctx)
		}
		seen[version.Version] = true
	}
	if !seen[0] {
		return nativeUnavailable(ctx)
	}
	statuses, err := provider.Status(ctx)
	if err != nil {
		return err
	}
	for _, status := range statuses {
		if status.State != goose.StateApplied {
			return nativeUnavailable(ctx)
		}
	}
	var valid bool
	err = db.QueryRowContext(ctx, `SELECT
        (SELECT count(*) FROM pg_attribute WHERE attrelid='knowl_embedding_chunks'::regclass AND attnum>0 AND NOT attisdropped)=8
        AND NOT EXISTS (
            SELECT 1 FROM (VALUES ('scope','text'::regtype::oid),('space','text'::regtype::oid),('page_id','text'::regtype::oid),('page_digest','text'::regtype::oid),('ordinal','int4'::regtype::oid),('content_hash','text'::regtype::oid),('dimensions','int4'::regtype::oid),('vector',$1::oid)) expected(name,type_oid)
            LEFT JOIN pg_attribute a ON a.attrelid='knowl_embedding_chunks'::regclass AND a.attname=expected.name AND NOT a.attisdropped
            WHERE a.attnum IS NULL OR a.atttypid<>expected.type_oid OR NOT a.attnotnull OR a.atttypmod<>-1)
        AND EXISTS (SELECT 1 FROM pg_attribute a JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE a.attrelid='knowl_embedding_state'::regclass AND a.attname='native_format' AND a.atttypid='int4'::regtype AND a.attnotnull AND NOT a.attisdropped AND pg_get_expr(d.adbin,d.adrelid)='0')
        AND (SELECT count(*) FROM pg_constraint WHERE conrelid='knowl_embedding_chunks'::regclass)=10
        AND NOT EXISTS (
            SELECT 1 FROM (VALUES
              ('knowl_embedding_chunks_scope_fkey','FOREIGN KEY (scope) REFERENCES knowl_embedding_state(scope) ON DELETE CASCADE'),
              ('knowl_embedding_chunks_space_check','CHECK ((octet_length(space) = 64))'),
              ('knowl_embedding_chunks_page_id_check','CHECK (((octet_length(page_id) >= 1) AND (octet_length(page_id) <= 4096)))'),
              ('knowl_embedding_chunks_page_digest_check','CHECK (((octet_length(page_digest) >= 1) AND (octet_length(page_digest) <= 256)))'),
              ('knowl_embedding_chunks_ordinal_check','CHECK (((ordinal >= 0) AND (ordinal <= 8191)))'),
              ('knowl_embedding_chunks_content_hash_check','CHECK ((octet_length(content_hash) = 64))'),
              ('knowl_embedding_chunks_dimensions_check','CHECK (((dimensions >= 1) AND (dimensions <= 4096)))'),
              ('knowl_embedding_chunks_vector_check',format('CHECK ((%s(vector) = dimensions))',CASE WHEN pg_function_is_visible($2::oid) THEN 'vector_dims' ELSE quote_ident($3)||'.vector_dims' END)),
              ('knowl_embedding_chunks_pkey','PRIMARY KEY (scope, space, page_id, ordinal)'),
              ('knowl_embedding_chunks_scope_page_id_fkey','FOREIGN KEY (scope, page_id) REFERENCES knowl_pages(scope, page_id) ON DELETE CASCADE')
            ) expected(name,definition)
            LEFT JOIN pg_constraint c ON c.conrelid='knowl_embedding_chunks'::regclass AND c.conname=expected.name
            WHERE c.oid IS NULL OR NOT c.convalidated OR pg_get_constraintdef(c.oid)<>expected.definition)`, capability.typeOID, capability.dimsOID, capability.schema).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return nativeUnavailable(ctx)
	}
	return nil
}
