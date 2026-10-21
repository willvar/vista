package instance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"vista/internal/appdirs"
)

func TestDirUsesApplicationStateDirectory(t *testing.T) {
	want, err := appdirs.State()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Dir()
	if err != nil || got != want {
		t.Fatalf("Dir() = %q, %v, want %q", got, err, want)
	}
}

func TestInstanceRecordHasReadAndWriteBounds(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, Info{PID: 123, Address: "http://127.0.0.1:1234"}); err != nil {
		t.Fatal(err)
	}
	if info, err := Read(dir); err != nil || info.PID != 123 || info.Address != "http://127.0.0.1:1234" {
		t.Fatalf("small instance record: %+v, %v", info, err)
	}
	if err := Write(dir, Info{PID: 123, Address: strings.Repeat("x", maxInfoBytes)}); err == nil {
		t.Fatal("oversized instance record was written")
	}
	if err := os.WriteFile(filepath.Join(dir, "instance.json"), []byte(strings.Repeat("x", maxInfoBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err == nil || !strings.Contains(err.Error(), "exceeds 4 KiB") {
		t.Fatalf("oversized instance record was read: %v", err)
	}
}
