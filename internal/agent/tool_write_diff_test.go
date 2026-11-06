package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vista/internal/tools"
	"vista/internal/workerproto"
)

func TestWriteFileDiffIsDurableAndAbsentFromModelContext(t *testing.T) {
	db, emit := outputToolStore(t)
	path := filepath.Join(t.TempDir(), "source.txt")
	arguments, _ := json.Marshal(map[string]string{"path": path, "expectedSha256": "missing", "content": "original evidence\n"})
	runtime := &toolRuntime{parent: workerproto.Command{RunID: "run", SessionID: "session"}, emit: emit}
	output, err := runtime.writeFile(context.Background(), scriptedToolCall("write-1", "write_file", string(arguments)))
	ref := resultOutputRef(output)
	if err != nil || ref == nil || ref.ArtifactID == "" || strings.Contains(output, "original evidence") {
		t.Fatalf("write must return a small artifact reference: %s, %v", output, err)
	}
	artifact, err := db.GetArtifact(context.Background(), "session", ref.ArtifactID)
	if err != nil || !artifact.Complete || artifact.Status != "succeeded" {
		t.Fatalf("diff not persisted before tool success: %+v, %v", artifact, err)
	}
	if err := os.WriteFile(path, []byte("later external change"), 0600); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(artifact.OutputPath)
	var result tools.FileWrite
	if err != nil || json.Unmarshal(saved, &result) != nil || !result.Diff.Created || !strings.Contains(result.Diff.Unified, "+original evidence") || strings.Contains(string(saved), "later external change") {
		t.Fatalf("diff followed the live filesystem: %s, %v", saved, err)
	}
}

func TestWriteFilePresentationFailureDoesNotRepeatSideEffect(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "source.txt")
	arguments, _ := json.Marshal(map[string]string{"path": path, "expectedSha256": "missing", "content": "committed"})
	runtime := &toolRuntime{parent: workerproto.Command{RunID: "run"}, emit: func(workerproto.Result) error { return errors.New("artifact unavailable") }}
	output, err := runtime.writeFile(context.Background(), scriptedToolCall("write-1", "write_file", string(arguments)))
	var result tools.FileWrite
	if err != nil || json.Unmarshal([]byte(output), &result) != nil || result.SHA256 == "" || result.Diff.Omitted != "unavailable" {
		t.Fatalf("committed write became a retryable error: %s, %v", output, err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "committed" {
		t.Fatalf("write not committed: %q, %v", content, err)
	}
}
