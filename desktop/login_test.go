package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoginFileOwnsOnlyItsEntry(t *testing.T) {
	dir := t.TempDir()
	f := loginFile{filepath.Join(dir, "autostart", "vista"), "\nowned by vista\n"}
	assert := func(want bool) {
		t.Helper()
		if got, err := f.enabled(); err != nil || got != want {
			t.Fatalf("enabled=%v, error=%v, want %v", got, err, want)
		}
	}
	assert(false)
	if err := f.set(true, []byte(f.marker)); err != nil {
		t.Fatal(err)
	}
	assert(true)
	if err := f.set(false, nil); err != nil {
		t.Fatal(err)
	}
	assert(false)
	if err := os.WriteFile(f.path, []byte("another entry"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.set(true, []byte(f.marker)); err == nil {
		t.Fatal("replaced unknown login entry")
	}
	if err := f.set(false, nil); err == nil {
		t.Fatal("deleted unknown login entry")
	}
}
