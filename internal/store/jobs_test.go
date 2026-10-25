package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackgroundJobKeepsSessionRunningAfterReply(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const session = "00000000-0000-4000-8000-000000000006"
	if _, err := s.Create(ctx, session, "background"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, session, "run", "message", "start"); err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "job", SessionID: session, RunID: "run", Directory: "/tmp", Command: "sleep 10", OutputPath: "/tmp/job.log", Status: "starting", ExitCode: -1, StartedAt: "2026-10-18T10:00:00Z", ExpiresAt: "2026-10-18T11:00:00Z"}
	if event, err := s.RecordJob(ctx, job); err != nil || event == nil {
		t.Fatalf("job start: %+v %v", event, err)
	}
	job.Status = "running"
	if _, err := s.RecordJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	job.Bytes = 1234
	if event, err := s.RecordJob(ctx, job); err != nil || event == nil || event.Kind != "job_output" || strings.Contains(string(event.Data), "sleep 10") {
		t.Fatalf("output growth must be a compact metadata event: %+v %v", event, err)
	}
	if _, err := s.FinishRun(ctx, session, "run", "answer", "succeeded", "job started"); err != nil {
		t.Fatal(err)
	}
	counts, err := s.SessionCounts(ctx)
	if err != nil || counts.Running != 1 || counts.Jobs != 1 {
		t.Fatalf("background state lost after reply: %+v %v", counts, err)
	}
	items, err := s.List(ctx)
	if err != nil || len(items) != 1 || !items[0].Running || items[0].Executing {
		t.Fatalf("wrong Session state: %+v %v", items, err)
	}
	job.Status, job.Complete, job.ExitCode = "succeeded", true, 0
	job.Signal, job.TerminationReason = "", "exit"
	if _, err := s.RecordJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	counts, err = s.SessionCounts(ctx)
	if err != nil || counts.Running != 0 || counts.Jobs != 0 {
		t.Fatalf("finished job left Session running: %+v %v", counts, err)
	}
	got, err := s.GetJob(ctx, session, job.ID)
	if err != nil || got.StartedAt != job.StartedAt || got.ExpiresAt != job.ExpiresAt || got.TerminationReason != job.TerminationReason || got.FinishedAt == "" {
		t.Fatalf("job metadata not saved: %+v %v", got, err)
	}
}

func TestRestartMarksUnfinishedJobInterrupted(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const session = "00000000-0000-4000-8000-000000000007"
	if _, err := s.Create(ctx, session, "crash"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, session, "run", "message", "start"); err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "job", SessionID: session, RunID: "run", Directory: "/tmp", Command: "sleep 10", OutputPath: "/tmp/job.log", Status: "starting", ExitCode: -1, ExpiresAt: "2026-10-18T11:00:00Z"}
	if _, err := s.RecordJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	events, err := s.InterruptJobs(ctx, session, "Worker died")
	if err != nil || len(events) != 1 {
		t.Fatalf("job interruption: %+v %v", events, err)
	}
	got, err := s.GetJob(ctx, session, job.ID)
	if err != nil || got.Status != "interrupted" || got.Error != "Worker died" || got.TerminationReason != "interrupted" || got.ExpiresAt != job.ExpiresAt {
		t.Fatalf("job state: %+v %v", got, err)
	}
}

func TestJobListKeepsActiveJobsBeyondRecentHistory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vista.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	if _, err := s.Create(ctx, "session", "process history"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, "session", "run", "message", "start"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= 200; i++ {
		job := Job{ID: fmt.Sprintf("job-%d", i), SessionID: "session", RunID: "run", Status: "starting", ExpiresAt: "2026-10-19T11:00:00Z"}
		if _, err := s.RecordJob(ctx, job); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			continue
		}
		job.Status, job.Signal, job.TerminationReason = "failed", "SIGTERM", "signal"
		if _, err := s.RecordJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := s.Jobs(ctx, "session")
	if err != nil || len(jobs) != 200 || jobs[0].ID != "job-0" || jobs[1].Signal != "SIGTERM" || jobs[1].ExpiresAt == "" {
		t.Fatalf("active job or stored metadata lost: count=%d %v", len(jobs), err)
	}
}
