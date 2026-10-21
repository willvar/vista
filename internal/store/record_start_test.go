package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordStartFailureDoesNotCommitEvent(t *testing.T) {
	for _, table := range []string{"jobs", "artifacts"} {
		t.Run(table, func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if _, err := s.Create(ctx, "session", "write failure"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.BeginRun(ctx, "session", "run", "message", "start"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, "CREATE TRIGGER reject_start BEFORE INSERT ON "+table+" BEGIN SELECT RAISE(FAIL,'injected insert failure'); END"); err != nil {
				t.Fatal(err)
			}
			record := func() (*Event, error) {
				if table == "jobs" {
					return s.RecordJob(ctx, Job{ID: "job", SessionID: "session", RunID: "run", Status: "starting"})
				}
				return s.RecordArtifact(ctx, Artifact{ID: "artifact", SessionID: "session", RunID: "run", Status: "started", OutputPath: filepath.Join(t.TempDir(), "output.txt")})
			}
			var eventsBefore int
			if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events").Scan(&eventsBefore); err != nil {
				t.Fatal(err)
			}
			event, err := record()
			if err == nil || !strings.Contains(err.Error(), "injected insert failure") || event != nil {
				t.Fatalf("failed insert acknowledged: event=%+v err=%v", event, err)
			}
			var rows, eventsAfter int
			if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events").Scan(&eventsAfter); err != nil {
				t.Fatal(err)
			}
			if rows != 0 || eventsAfter != eventsBefore {
				t.Fatalf("failed registration left rows=%d events=%d (was %d)", rows, eventsAfter, eventsBefore)
			}
			if _, err := s.db.ExecContext(ctx, "DROP TRIGGER reject_start"); err != nil {
				t.Fatal(err)
			}
			if event, err := record(); err != nil || event == nil {
				t.Fatalf("registration did not recover: event=%+v err=%v", event, err)
			}
		})
	}
}
