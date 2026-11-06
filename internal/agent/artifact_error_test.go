package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"vista/internal/workerproto"
)

func TestPartialArtifactErrorFitsWorkerBoundary(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	t.Setenv("HOME", root)
	cause := errors.New(strings.Repeat("界", 300))
	var terminal *workerproto.Artifact
	ref, err := recordToolOutputStream(context.Background(), "run", "call", func(w io.Writer) error {
		_, _ = io.WriteString(w, "partial evidence")
		return cause
	}, func(result workerproto.Result) error {
		encoded, err := json.Marshal(result)
		var decoded workerproto.Result
		if err != nil || json.Unmarshal(encoded, &decoded) != nil {
			t.Fatalf("invalid artifact frame: %v", err)
		}
		if decoded.Artifact != nil && decoded.Status == "failed" {
			terminal = decoded.Artifact
			if len(terminal.Error) > workerproto.MaxErrorBytes || !utf8.ValidString(result.Artifact.Error) {
				t.Errorf("artifact error violates the receiver's byte/UTF-8 budget: %d bytes", len(terminal.Error))
			}
		}
		return nil
	})
	if !errors.Is(err, cause) || ref == nil || terminal == nil || terminal.Complete || terminal.Bytes != int64(len("partial evidence")) {
		t.Fatalf("partial output or original error lost: ref=%+v terminal=%+v error=%v", ref, terminal, err)
	}
	data, err := os.ReadFile(ref.OutputPath)
	if err != nil || string(data) != "partial evidence" {
		t.Fatalf("partial evidence was not retained: %q, %v", data, err)
	}
}
