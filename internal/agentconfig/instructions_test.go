package agentconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstructionsRoundTripAndValidation(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := Read(dir); err != nil || got != "" {
		t.Fatalf("missing instructions: %q, %v", got, err)
	}
	// Global AGENTS.md files are user-authored documents, not short labels.
	want := strings.Repeat("Reply in Chinese.\n保留完整规则。\n", 2000)
	if err := Write(dir, want); err != nil {
		t.Fatal(err)
	}
	if got, err := Read(dir); err != nil || got != want {
		t.Fatalf("saved instructions: %q, %v", got, err)
	}
	if info, err := os.Stat(filepath.Join(dir, filename)); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("instructions file permissions: %v, %v", info, err)
	}
	if err := Write(dir, "invalid UTF-8: \xff"); err == nil {
		t.Fatal("invalid UTF-8 instructions accepted")
	}
	if got, err := Read(dir); err != nil || got != want {
		t.Fatalf("rejected write changed saved instructions: %q, %v", got, err)
	}
	if err := Write(dir, ""); err != nil {
		t.Fatal(err)
	}
	if got, err := Read(dir); err != nil || got != "" {
		t.Fatalf("cleared instructions: %q, %v", got, err)
	}
}
