package knowl

import (
	"strings"
	"testing"
)

func TestStorageConfigNormalize(t *testing.T) {
	workspace := t.TempDir()
	secretDSN := "postgres://user:super-secret@localhost/knowl"
	tests := []struct {
		name       string
		config     StorageConfig
		wantDriver string
		wantPath   string
		wantDSN    string
		wantErr    string
	}{
		{
			name:       "sqlite path",
			config:     StorageConfig{Type: StoreSQLite, SQLite: &SQLiteConfig{Path: ".knowl/custom.db"}},
			wantDriver: StoreSQLite,
			wantPath:   workspace + "/.knowl/custom.db",
		},
		{
			name:       "sqlite default path",
			config:     StorageConfig{Type: StoreSQLite, SQLite: &SQLiteConfig{}},
			wantDriver: StoreSQLite,
			wantPath:   workspace + "/.knowl/knowl.sqlite",
		},
		{
			name:       "postgres",
			config:     StorageConfig{Type: StorePostgres, Postgres: &PostgresConfig{DSN: secretDSN}},
			wantDriver: StorePostgres,
			wantDSN:    secretDSN,
		},
		{
			name:    "sqlite block missing",
			config:  StorageConfig{Type: StoreSQLite},
			wantErr: "knowl.storage.sqlite",
		},
		{
			name:    "postgres block missing",
			config:  StorageConfig{Type: StorePostgres},
			wantErr: "knowl.storage.postgres",
		},
		{
			name:    "postgres dsn missing",
			config:  StorageConfig{Type: StorePostgres, Postgres: &PostgresConfig{}},
			wantErr: "knowl.storage.postgres.dsn",
		},
		{
			name:    "extra postgres block",
			config:  StorageConfig{Type: StoreSQLite, SQLite: &SQLiteConfig{}, Postgres: &PostgresConfig{DSN: secretDSN}},
			wantErr: "knowl.storage.postgres",
		},
		{
			name:    "extra sqlite block",
			config:  StorageConfig{Type: StorePostgres, SQLite: &SQLiteConfig{}, Postgres: &PostgresConfig{DSN: secretDSN}},
			wantErr: "knowl.storage.sqlite",
		},
		{
			name:    "unsupported type",
			config:  StorageConfig{Type: "mysql", SQLite: &SQLiteConfig{}},
			wantErr: "unsupported knowl.storage.type",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.config.Normalize(workspace)
			if test.wantErr != "" {
				if err == nil {
					t.Fatal("Normalize() error = nil, want error")
				}
				if !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("Normalize() error = %q, want substring %q", err, test.wantErr)
				}
				if strings.Contains(err.Error(), secretDSN) {
					t.Fatalf("Normalize() leaked DSN: %q", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Normalize() error: %v", err)
			}
			if got.Driver != test.wantDriver || got.Path != test.wantPath || got.DSN != test.wantDSN {
				t.Fatalf("Normalize() = %#v, want driver=%q path=%q dsn=%q", got, test.wantDriver, test.wantPath, test.wantDSN)
			}
		})
	}
}
