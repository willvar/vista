package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkerStoreReadsWALWithoutCreatingOrWritingDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vista data #1.db")
	server, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	const id = "00000000-0000-4000-8000-000000000033"
	if _, err := server.Create(ctx, id, "visible in WAL"); err != nil {
		t.Fatal(err)
	}
	worker, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	item, err := worker.Get(ctx, id)
	if err != nil || item.Title != "visible in WAL" {
		t.Fatalf("read-only WAL read: %+v, %v", item, err)
	}
	if _, err := worker.Create(ctx, "other", "must fail"); err == nil {
		t.Fatal("read-only Worker connection accepted a write")
	}
	missing := filepath.Join(t.TempDir(), "absent.db")
	if _, err := OpenReadOnly(missing); err == nil {
		t.Fatal("read-only Worker connection created a missing database")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing database was created: %v", err)
	}
}
