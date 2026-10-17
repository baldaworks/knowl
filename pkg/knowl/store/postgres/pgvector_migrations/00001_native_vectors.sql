-- +goose Up
-- +goose StatementBegin
DO $$
DECLARE
    extension_schema text;
    vector_type oid;
BEGIN
    -- Goose has initialized the standard history under its session lock.
    -- Reject unknown/corrupt pending history without trying to repair it.
    IF (SELECT count(*) FROM goose_pgvector_db_version)<>1
       OR EXISTS (SELECT 1 FROM goose_pgvector_db_version WHERE version_id<>0 OR NOT is_applied) THEN
        RAISE EXCEPTION 'Unrecognized optional migration history' USING ERRCODE='55000';
    END IF;
    SELECT n.nspname,t.oid INTO STRICT extension_schema,vector_type
    FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace
    JOIN pg_type t ON t.typnamespace=n.oid AND t.typname='vector'
    JOIN pg_depend d ON d.classid='pg_type'::regclass AND d.objid=t.oid AND d.refclassid='pg_extension'::regclass AND d.refobjid=e.oid AND d.deptype='e'
    WHERE e.extname='vector';

    -- Fail before discarding state if the recognized base schema is absent.
    IF NOT EXISTS (SELECT 1 FROM pg_attribute a JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE a.attrelid='knowl_embedding_state'::regclass AND a.attname='native_format' AND a.atttypid='int4'::regtype AND a.attnotnull AND NOT a.attisdropped AND pg_get_expr(d.adbin,d.adrelid)='0')
       OR (SELECT count(*) FROM pg_attribute WHERE attrelid='knowl_embedding_chunks'::regclass AND attnum>0 AND NOT attisdropped) <> 8
       OR EXISTS (
           SELECT 1 FROM (VALUES ('scope','text'::regtype::oid),('space','text'::regtype::oid),('page_id','text'::regtype::oid),('page_digest','text'::regtype::oid),('ordinal','int4'::regtype::oid),('content_hash','text'::regtype::oid),('dimensions','int4'::regtype::oid),('vector','bytea'::regtype::oid)) expected(name,type_oid)
           LEFT JOIN pg_attribute a ON a.attrelid='knowl_embedding_chunks'::regclass AND a.attname=expected.name AND NOT a.attisdropped
           WHERE a.attnum IS NULL OR a.atttypid<>expected.type_oid OR NOT a.attnotnull OR a.atttypmod<>-1)
       OR (SELECT count(*) FROM pg_constraint WHERE conrelid='knowl_embedding_chunks'::regclass) <> 10
       OR EXISTS (
           SELECT 1 FROM (VALUES
               ('knowl_embedding_chunks_scope_fkey','FOREIGN KEY (scope) REFERENCES knowl_embedding_state(scope) ON DELETE CASCADE'),
               ('knowl_embedding_chunks_space_check','CHECK ((octet_length(space) = 64))'),
               ('knowl_embedding_chunks_page_id_check','CHECK (((octet_length(page_id) >= 1) AND (octet_length(page_id) <= 4096)))'),
               ('knowl_embedding_chunks_page_digest_check','CHECK (((octet_length(page_digest) >= 1) AND (octet_length(page_digest) <= 256)))'),
               ('knowl_embedding_chunks_ordinal_check','CHECK (((ordinal >= 0) AND (ordinal <= 8191)))'),
               ('knowl_embedding_chunks_content_hash_check','CHECK ((octet_length(content_hash) = 64))'),
               ('knowl_embedding_chunks_dimensions_check','CHECK (((dimensions >= 1) AND (dimensions <= 4096)))'),
               ('knowl_embedding_chunks_check','CHECK ((octet_length(vector) = (dimensions * 4)))'),
               ('knowl_embedding_chunks_pkey','PRIMARY KEY (scope, space, page_id, ordinal)'),
               ('knowl_embedding_chunks_scope_page_id_fkey','FOREIGN KEY (scope, page_id) REFERENCES knowl_pages(scope, page_id) ON DELETE CASCADE')
           ) expected(name,definition)
           LEFT JOIN pg_constraint c ON c.conrelid='knowl_embedding_chunks'::regclass AND c.conname=expected.name
           WHERE c.oid IS NULL OR NOT c.convalidated OR pg_get_constraintdef(c.oid)<>expected.definition
       ) THEN
        RAISE EXCEPTION 'Unrecognized base embedding schema' USING ERRCODE='55000';
    END IF;

    DELETE FROM knowl_embedding_state;
    ALTER TABLE knowl_embedding_chunks DROP CONSTRAINT knowl_embedding_chunks_check;
    EXECUTE format('ALTER TABLE knowl_embedding_chunks ALTER COLUMN vector TYPE %I.vector USING NULL::%I.vector',extension_schema,extension_schema);
    EXECUTE format('ALTER TABLE knowl_embedding_chunks ADD CONSTRAINT knowl_embedding_chunks_vector_check CHECK (%I.vector_dims(vector)=dimensions)',extension_schema);
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
DECLARE
    vector_type oid;
BEGIN
    SELECT t.oid INTO STRICT vector_type FROM pg_extension e
    JOIN pg_type t ON t.typnamespace=e.extnamespace AND t.typname='vector'
    JOIN pg_depend d ON d.classid='pg_type'::regclass AND d.objid=t.oid AND d.refclassid='pg_extension'::regclass AND d.refobjid=e.oid AND d.deptype='e'
    WHERE e.extname='vector';
    IF NOT EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='knowl_embedding_chunks'::regclass AND attname='vector' AND atttypid=vector_type AND atttypmod=-1 AND attnotnull AND NOT attisdropped)
       OR NOT EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='knowl_embedding_state'::regclass AND attname='native_format' AND atttypid='int4'::regtype AND NOT attisdropped) THEN
        RAISE EXCEPTION 'Unrecognized native embedding schema' USING ERRCODE='55000';
    END IF;
    DELETE FROM knowl_embedding_state;
    ALTER TABLE knowl_embedding_chunks DROP CONSTRAINT knowl_embedding_chunks_vector_check;
    ALTER TABLE knowl_embedding_chunks ALTER COLUMN vector TYPE bytea USING NULL::bytea;
    ALTER TABLE knowl_embedding_chunks ADD CONSTRAINT knowl_embedding_chunks_check CHECK (octet_length(vector)=dimensions*4);
END $$;
-- +goose StatementEnd
