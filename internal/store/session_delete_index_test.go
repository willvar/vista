package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func requireIndexedLookups(t *testing.T, s *Store, query string, tables []string, column string, args ...any) {
	t.Helper()
	rows, err := s.db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, aux int
		var detail string
		if err := rows.Scan(&id, &parent, &aux, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		indexed := false
		for _, entry := range plan {
			indexed = indexed || strings.HasPrefix(entry, "SEARCH "+table+" ") && strings.Contains(entry, "("+column+"=?)")
		}
		if !indexed {
			t.Errorf("%s has no indexed %s lookup: %v", table, column, plan)
		}
	}
}

func TestSessionDeletionIndexesOnFreshAndExistingDatabases(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "vista.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	checkPlans := func(t *testing.T) {
		requireIndexedLookups(t, s, `DELETE FROM runs WHERE id=?`,
			[]string{"messages", "model_output_chunks", "artifacts", "runtime_events", "jobs", "queued_inputs"}, "run_id", "run-a")
		requireIndexedLookups(t, s, `SELECT EXISTS(SELECT 1 FROM jobs WHERE output_path=?) OR EXISTS(SELECT 1 FROM artifacts WHERE output_path=?)`,
			[]string{"jobs", "artifacts"}, "output_path", "shared.log", "shared.log")
	}
	t.Run("fresh", checkPlans)
	for _, id := range []string{"a", "b"} {
		if _, err := s.Create(ctx, id, "conversation "+id); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.BeginRun(ctx, id, "run-"+id, "message-"+id, "prompt"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.RecordArtifact(ctx, Artifact{ID: "artifact-" + id, SessionID: id, RunID: "run-" + id, OutputPath: "shared.log", Status: "started"}); err != nil {
			t.Fatal(err)
		}
	}
	// Model an existing database before these indexes were added.
	for _, name := range []string{"messages_by_run", "model_output_chunks_by_run", "artifacts_by_run", "runtime_events_by_run", "jobs_by_run", "queued_inputs_by_run", "jobs_by_output_path", "artifacts_by_output_path"} {
		if _, err := s.db.ExecContext(ctx, "DROP INDEX IF EXISTS "+name); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s = reopened
	t.Run("existing", checkPlans)
	if err := s.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if paths, err := s.ArtifactPaths(ctx, "a"); err != nil || len(paths) != 0 {
		t.Fatalf("deleted conversation retained artifacts: %v, %v", paths, err)
	}
	if used, err := s.BranchOutputReferenced(ctx, "shared.log"); err != nil || !used {
		t.Fatalf("the survivor's output must still be referenced: %v, %v", used, err)
	}
	if err := s.Delete(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if used, err := s.BranchOutputReferenced(ctx, "shared.log"); err != nil || used {
		t.Fatalf("no output reference should survive both deletes: %v, %v", used, err)
	}
}
