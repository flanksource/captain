package database

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"gorm.io/gorm"
)

func TestOpenMigratesThenOpensStandalonePool(t *testing.T) {
	t.Parallel()

	var calls []string
	opened := &gorm.DB{}
	db, err := open(t.Context(), dependencies{
		migrate: func(_ context.Context, dsn, schemaName string) error {
			calls = append(calls, "migrate:"+dsn+":"+schemaName)
			return nil
		},
		open: func(dsn string, _ *gorm.Config) (*gorm.DB, error) {
			calls = append(calls, "open:"+dsn)
			return opened, nil
		},
	}, WithDSN(" postgres://captain "), WithMigrations())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if db.Gorm() != opened || !db.owned {
		t.Fatalf("database = %+v, want owned standalone pool", db)
	}
	want := []string{"migrate:postgres://captain:public", "open:postgres://captain"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestOpenMigratesThenReusesInjectedPool(t *testing.T) {
	t.Parallel()

	shared := &gorm.DB{}
	var calls []string
	db, err := open(t.Context(), dependencies{
		migrate: func(_ context.Context, dsn, schemaName string) error {
			calls = append(calls, "migrate:"+dsn+":"+schemaName)
			return nil
		},
		open: func(string, *gorm.Config) (*gorm.DB, error) {
			t.Fatal("injected pool must not open another application pool")
			return nil, nil
		},
	}, WithGorm(shared), WithDSN("postgres://shared"), WithMigrations())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if db.Gorm() != shared || db.owned {
		t.Fatalf("database = %+v, want non-owning injected pool", db)
	}
	if want := []string{"migrate:postgres://shared:public"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close injected database: %v", err)
	}
}

func TestOpenUsesPreMigratedInjectedPoolWithoutDSN(t *testing.T) {
	t.Parallel()

	shared := &gorm.DB{}
	db, err := open(t.Context(), dependencies{
		migrate: func(context.Context, string, string) error {
			t.Fatal("pre-migrated injected pool must not run migrations")
			return nil
		},
		open: func(string, *gorm.Config) (*gorm.DB, error) {
			t.Fatal("injected pool must not be reopened")
			return nil, nil
		},
	}, WithGorm(shared))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if db.Gorm() != shared {
		t.Fatal("Open did not retain injected pool identity")
	}
}

func TestOpenStopsWhenMigrationFails(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("migration failed")
	_, err := open(t.Context(), dependencies{
		migrate: func(context.Context, string, string) error { return wantErr },
		open: func(string, *gorm.Config) (*gorm.DB, error) {
			t.Fatal("pool must not open after migration failure")
			return nil, nil
		},
	}, WithDSN("postgres://captain"), WithMigrations())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Open error = %v, want %v", err, wantErr)
	}
}

func TestOpenRequiresPoolOrDSN(t *testing.T) {
	t.Parallel()

	if _, err := Open(t.Context()); err == nil {
		t.Fatal("Open unexpectedly accepted an empty config")
	}
	if _, err := Use(nil); err == nil {
		t.Fatal("Use unexpectedly accepted a nil pool")
	}
}
