package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactPersistsPartialOutputAfterInterruptedRun(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := Open(filepath.Join(root, "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const session = "00000000-0000-4000-8000-000000000012"
	if _, err := s.Create(ctx, session, "artifact"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, session, "run", "user", "shell"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "partial.log")
	if err := os.WriteFile(path, []byte("partial output"), 0600); err != nil {
		t.Fatal(err)
	}
	item := Artifact{ID: "artifact", SessionID: session, RunID: "run", OutputPath: path, Status: "started"}
	if event, err := s.RecordArtifact(ctx, item); err != nil || event == nil {
		t.Fatalf("artifact start: %+v %v", event, err)
	}
	if _, err := s.InterruptRunning(ctx, session, "Worker crashed"); err != nil {
		t.Fatal(err)
	}
	events, err := s.InterruptArtifacts(ctx, session, "Worker crashed")
	if err != nil || len(events) != 1 || events[0].Kind != "artifact" {
		t.Fatalf("artifact interruption: %+v %v", events, err)
	}
	got, err := s.GetArtifact(ctx, session, item.ID)
	if err != nil || got.Status != "failed" || got.Complete || got.Bytes != int64(len("partial output")) || got.Error != "Worker crashed" {
		t.Fatalf("partial artifact lost: %+v %v", got, err)
	}
	paths, err := s.ArtifactPaths(ctx, session)
	if err != nil || len(paths) != 1 || paths[0] != path {
		t.Fatalf("artifact paths: %+v %v", paths, err)
	}
}
