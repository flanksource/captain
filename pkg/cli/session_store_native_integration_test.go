package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/captain/migrations"
	commonsdb "github.com/flanksource/commons-db/db"
)

func TestLegacySessionStoreDoesNotMutateAuthoritativeSchema(t *testing.T) {
	if os.Getenv("CAPTAIN_DB_EMBEDDED_TEST") == "" {
		t.Skip("set CAPTAIN_DB_EMBEDDED_TEST=1 to run embedded-postgres migration tests")
	}

	dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
		DataDir:  filepath.Join(t.TempDir(), "postgres"),
		Database: "captain_legacy_guard",
	})
	if err != nil {
		t.Fatalf("start embedded postgres: %v", err)
	}
	t.Cleanup(func() {
		if err := stop(); err != nil {
			t.Errorf("stop embedded postgres: %v", err)
		}
	})

	if err := migrations.Apply(t.Context(), dsn); err != nil {
		t.Fatalf("apply Captain migrations: %v", err)
	}
	gormDB, err := commonsdb.NewGorm(dsn, commonsdb.DefaultGormConfig())
	if err != nil {
		t.Fatalf("open GORM: %v", err)
	}
	sqlDB, err := gormDB.DB()
	if err != nil {
		t.Fatalf("access SQL pool: %v", err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close SQL pool: %v", err)
		}
	})

	if !hasAuthoritativeSessionSchema(gormDB) {
		t.Fatal("authoritative schema signature was not detected")
	}
	if store := openLegacySessionStore(gormDB); store != nil {
		t.Fatalf("legacy session store = %+v, want nil", store)
	}
	if err := sqlDB.PingContext(t.Context()); err != nil {
		t.Fatalf("non-owning legacy guard closed its caller's pool: %v", err)
	}
	if gormDB.Migrator().HasTable((StoredPrompt{}).TableName()) {
		t.Fatalf("legacy table %s was created in the authoritative database", (StoredPrompt{}).TableName())
	}

	var idType string
	if err := gormDB.Raw(`SELECT data_type
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'captain_sessions'
		  AND column_name = 'id'`).Scan(&idType).Error; err != nil {
		t.Fatalf("read captain_sessions.id type: %v", err)
	}
	if idType != "uuid" {
		t.Fatalf("captain_sessions.id type = %q, want uuid", idType)
	}

	owned, err := commonsdb.NewGorm(dsn, commonsdb.DefaultGormConfig())
	if err != nil {
		t.Fatalf("open owned GORM pool: %v", err)
	}
	ownedSQL, err := owned.DB()
	if err != nil {
		t.Fatalf("access owned SQL pool: %v", err)
	}
	if store := openOwnedLegacySessionStore(owned); store != nil {
		t.Fatalf("owned legacy session store = %+v, want nil", store)
	}
	if err := ownedSQL.PingContext(t.Context()); err == nil {
		t.Fatal("unused owned legacy session pool remains open")
	}
}
