package tools

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestApplyPatchClosesPreflightDescriptors(t *testing.T) {
	// A subprocess keeps the descriptor budget away from other Go tests.
	const child = "VISTA_TEST_PATCH_DESCRIPTORS"
	if os.Getenv(child) != "1" {
		command := exec.Command(os.Args[0], "-test.run=^TestApplyPatchClosesPreflightDescriptors$", "-test.count=1")
		command.Env = append(os.Environ(), child+"=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("descriptor-budget subprocess: %v\n%s", err, output)
		}
		return
	}
	root, state := t.TempDir(), t.TempDir()
	hashes := make(map[string]string)
	var patch strings.Builder
	for i := range 96 {
		name := fmt.Sprintf("f%03d.txt", i)
		if err := os.WriteFile(filepath.Join(root, name), []byte("a\n"), 0600); err != nil {
			t.Fatal(err)
		}
		hashes[name] = fmt.Sprintf("%x", sha256.Sum256([]byte("a\n")))
		fmt.Fprintf(&patch, "--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n-a\n+b\n", name, name)
	}
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}
	limit.Cur = min(limit.Max, 256)
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}
	result, err := ApplyPatch(context.Background(), state, PatchInput{Directory: root, Patch: patch.String(), ExpectedSHA256: hashes})
	if err != nil || len(result.Applied) != len(hashes) {
		t.Fatalf("patch exhausted descriptors: applied=%d err=%v", len(result.Applied), err)
	}
	for name := range hashes {
		if text, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(text) != "b\n" {
			t.Fatalf("incorrect committed content for %s: %q %v", name, text, err)
		}
	}
	if entries, err := os.ReadDir(filepath.Join(state, "outputs")); err != nil || len(entries) != 0 {
		t.Fatalf("staging files retained: %d %v", len(entries), err)
	}
}
