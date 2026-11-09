package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vista/internal/instance"
	"vista/internal/store"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

func outputToolStore(t *testing.T) (*store.Store, func(workerproto.Result) error) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stateDir, err := instance.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(stateDir, "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if _, err := db.Create(ctx, "session", "output"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.BeginRun(ctx, "session", "run", "user", "read output"); err != nil {
		t.Fatal(err)
	}
	return db, func(result workerproto.Result) error {
		if result.Artifact == nil {
			return nil
		}
		item := result.Artifact
		_, err := db.RecordArtifact(ctx, store.Artifact{ID: item.ID, SessionID: "session", RunID: result.RunID, CallID: item.CallID, TaskID: "main", OutputPath: item.OutputPath, Status: result.Status, Bytes: item.Bytes, Complete: item.Complete, Error: item.Error})
		return err
	}
}

func TestReadToolOutputRecoversArtifactByIDAndCall(t *testing.T) {
	_, emit := outputToolStore(t)
	ctx := context.Background()
	full := strings.Repeat("完整证据🙂\n", 8000)
	bounded, err := boundToolOutput(ctx, "run", "main/child/read-1", "mcp_call_tool", full, nil, emit)
	if err != nil || bounded.reference == nil {
		t.Fatalf("artifact: %+v, %v", bounded.reference, err)
	}
	if strings.Contains(bounded.model, bounded.reference.OutputPath) || !strings.Contains(bounded.model, "read_tool_output") {
		t.Fatal("model should see recovery by ID without a filesystem path")
	}
	parent := workerproto.Command{SessionID: "session", RunID: "run", TaskID: "main/child"}
	var recovered strings.Builder
	for offset := int64(0); offset < int64(len(full)); {
		arguments := fmt.Sprintf(`{"artifactId":%q,"offset":%d,"limit":32768}`, bounded.reference.ArtifactID, offset)
		output, err := readToolOutput(ctx, arguments, parent)
		var page tools.OutputPage
		if err != nil || json.Unmarshal([]byte(output), &page) != nil || page.NextOffset <= offset || exceedsToolBudget(output) {
			t.Fatalf("page %d: %v, %s", offset, err, output)
		}
		recovered.WriteString(page.Content)
		offset = page.NextOffset
	}
	if recovered.String() != full {
		t.Fatal("saved tool output did not round-trip")
	}
	output, err := readToolOutput(ctx, `{"callId":"read-1"}`, parent)
	if err != nil || !strings.Contains(output, bounded.reference.ArtifactID) {
		t.Fatalf("current subagent call ID not resolved: %s, %v", output, err)
	}
	parent.SessionID = "another-session"
	if _, err := readToolOutput(ctx, fmt.Sprintf(`{"artifactId":%q}`, bounded.reference.ArtifactID), parent); err == nil {
		t.Fatal("artifact crossed Session boundary")
	}
	parent.SessionID, parent.RunID = "session", "another-run"
	if _, err := readToolOutput(ctx, `{"callId":"read-1"}`, parent); err == nil {
		t.Fatal("call ID crossed run boundary")
	}
}

func TestLargePartialToolOutputKeepsFailureMetadata(t *testing.T) {
	output, _ := json.Marshal(map[string]any{"preview": strings.Repeat("partial line\n", 2100), "complete": false, "error": "shell stopped", "artifactId": "saved"})
	preview := modelToolPreview(string(output), &workerproto.OutputRef{ArtifactID: "saved", Bytes: int64(len(output))})
	var result struct {
		Complete          bool
		Error, ArtifactID string
	}
	if json.Unmarshal([]byte(preview), &result) != nil || result.Complete || result.Error != "shell stopped" || result.ArtifactID != "saved" {
		t.Fatalf("model preview lost partial failure metadata: %s", preview)
	}
}

func TestRuntimeToolPreviewPreservesStructuredStatusAndContinuation(t *testing.T) {
	output, _ := json.Marshal(map[string]any{
		"preview": strings.Repeat("shell evidence\n", 600), "complete": false, "exitCode": 7,
		"error": "command exited with status 7", "nextOffset": 8192, "availableBytes": 65536,
	})
	ref := &workerproto.OutputRef{ArtifactID: "saved", OutputPath: "/private/output", Bytes: 65536}
	preview := modelToolPreviewLimit(string(output), ref, 8<<10)
	var result struct {
		Complete                             bool
		ExitCode, NextOffset, AvailableBytes int
		ArtifactID, Error, Preview           string
	}
	if len(preview) > 8<<10 || json.Unmarshal([]byte(preview), &result) != nil || result.ExitCode != 7 || result.Complete || result.NextOffset != 8192 || result.AvailableBytes != 65536 || result.ArtifactID != "saved" || result.Error != "command exited with status 7" || result.Preview == "" || strings.Contains(preview, "/private/output") {
		t.Fatalf("runtime preview lost structured status: %s", preview)
	}
}

func TestReadToolOutputJobContinuationPreservesIdentityAndSessionOwnership(t *testing.T) {
	db, _ := outputToolStore(t)
	ctx := context.Background()
	state, err := instance.Dir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "outputs", "background.log")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	full := strings.Repeat("background evidence\n", 300)
	if err := os.WriteFile(path, []byte(full), 0600); err != nil {
		t.Fatal(err)
	}
	job := store.Job{ID: "background", SessionID: "session", RunID: "run", OutputPath: path, Status: "starting"}
	if _, err := db.RecordJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	job.Status, job.Complete, job.Bytes = "succeeded", true, int64(len(full))
	if _, err := db.RecordJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	parent := workerproto.Command{SessionID: "session", RunID: "later-run"}
	for _, search := range []bool{false, true} {
		var recovered strings.Builder
		var matches int
		for offset := int64(0); offset < int64(len(full)); {
			input := map[string]any{"jobId": job.ID, "offset": offset, "limit": 128}
			if search {
				input["pattern"], input["literal"] = "evidence", true
			}
			args, _ := json.Marshal(input)
			output, err := readToolOutput(ctx, string(args), parent)
			var page struct {
				tools.OutputPage
				JobID, ArtifactID string
				Complete          bool
				Count             int
			}
			if err != nil || json.Unmarshal([]byte(output), &page) != nil || page.JobID != job.ID || page.ArtifactID != "" || !page.Complete || page.NextOffset <= offset {
				t.Fatalf("job continuation (search=%v): %s %v", search, output, err)
			}
			recovered.WriteString(page.Content)
			matches += page.Count
			offset = page.NextOffset
		}
		if !search && recovered.String() != full {
			t.Fatal("background output did not round-trip")
		}
		if search && matches != 300 {
			t.Fatalf("search lost matches: %d", matches)
		}
	}
	parent.SessionID = "another-session"
	if _, err := readToolOutput(ctx, `{"jobId":"background"}`, parent); err == nil {
		t.Fatal("background output crossed Session boundary")
	}
}
