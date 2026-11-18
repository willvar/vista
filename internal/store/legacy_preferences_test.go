package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpeningDatabasePreservesLegacyPreferencesWithoutCreatingThemForNewStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vista.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var count int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='memories'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("fresh store contains retired preferences: %d %v", count, err)
	}
	if _, err := db.db.ExecContext(ctx, `CREATE TABLE memories (id TEXT PRIMARY KEY, text TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `INSERT INTO memories VALUES ('legacy','Keep this user data.','old','old')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var text string
	if err := db.db.QueryRowContext(ctx, `SELECT text FROM memories WHERE id='legacy'`).Scan(&text); err != nil || text != "Keep this user data." {
		t.Fatalf("opening store changed legacy data: %q %v", text, err)
	}
}
