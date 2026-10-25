package store

import (
	"context"
	"testing"
)

func TestSessionListSnapshotCarriesSavedBoundariesAndCurrentState(t *testing.T) {
	s := revertStore(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, "empty", "No events"); err != nil {
		t.Fatal(err)
	}
	_, started, err := s.BeginRun(ctx, "s", "r", "user", "Work")
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.ListSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Items) != 2 || before.ThroughSequences["s"] != started.Sequence || before.ThroughSequences["empty"] != 0 {
		t.Fatalf("missing list or boundaries: %+v", before)
	}
	finished, err := s.FinishRun(ctx, "s", "r", "answer", "succeeded", "Done")
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.ListSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range after.Items {
		if item.ID == "s" && (item.Executing || item.Running || item.LatestReplySequence != finished.Sequence || item.LatestRunStatus != "succeeded") {
			t.Fatalf("stale list metadata: %+v", item)
		}
	}
	if after.ThroughSequences["s"] != finished.Sequence || before.ThroughSequences["s"] != started.Sequence {
		t.Fatal("snapshot boundary changed or failed to advance")
	}
	if err := s.Delete(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	after, err = s.ListSnapshot(ctx)
	if err != nil || len(after.Items) != 1 || len(after.ThroughSequences) != 1 {
		t.Fatalf("deleted session retained: %+v, %v", after, err)
	}
}
