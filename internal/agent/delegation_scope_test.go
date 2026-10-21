package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"vista/internal/workerproto"
)

func TestDelegationRunBoundariesAndSessionTaskIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registry := newDelegationRegistry()
	registry.sessionCtx = ctx
	defer registry.close()
	first, next := registry.forRun("first"), registry.forRun("next")
	oldRequired, err := first.start(ctx, "main", "same", func(ctx context.Context, _ string) (string, *workerproto.OutputRef, error) {
		<-ctx.Done()
		return "", nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	nextRequired, err := next.start(ctx, "main", "same", func(context.Context, string) (string, *workerproto.OutputRef, error) {
		return "next report", &workerproto.OutputRef{RunID: "next", Turn: 1, Bytes: 11}, nil
	})
	if err != nil || nextRequired.TaskID == oldRequired.TaskID {
		t.Fatalf("reused provider call ID overwrote a session task: %+v %v", nextRequired, err)
	}
	results, err := next.joinRequired(ctx, "main")
	if err != nil || len(results) != 1 || results[0].TaskID != nextRequired.TaskID || next.hasUncollectedRequired("main") {
		t.Fatalf("join included another run: %+v %v", results, err)
	}
	next.closeChildren("main")
	if err := registry.tasks[oldRequired.TaskID].ctx.Err(); err != nil {
		t.Fatalf("next-run cleanup canceled an older task: %v", err)
	}
	zero := int64(0)
	if _, err := next.await(ctx, "main", []string{oldRequired.TaskID}, &zero); err == nil {
		t.Fatal("old required task crossed its run boundary")
	}
	first.closeChildren("main")
	result, err := first.await(ctx, "main", []string{oldRequired.TaskID}, nil)
	if err != nil || result.Status != "canceled" {
		t.Fatalf("original owner lost its required task: %+v %v", result, err)
	}
	if next.hasUncollectedRequired("main") {
		t.Fatal("uncollected failed/canceled report from an earlier run blocked finalization")
	}
}

func TestDetachedReportsRetainFailureAndOwnershipAcrossRuns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registry := newDelegationRegistry()
	registry.sessionCtx = ctx
	defer registry.close()
	first, next := registry.forRun("first"), registry.forRun("next")
	task, err := first.startWithJoin(ctx, "main", "failed", "detached", func(context.Context, string) (string, *workerproto.OutputRef, error) {
		return "", nil, errors.New("child failed")
	})
	if err != nil {
		t.Fatal(err)
	}
	first.wg.Wait() // The foreground run and all its work can be retired.
	for _, parent := range []string{"sibling", "main/another-child"} {
		if _, err := next.await(ctx, parent, []string{task.TaskID}, nil); err == nil {
			t.Fatalf("unrelated owner %q collected a detached report", parent)
		}
		if _, err := next.cancel(ctx, parent, task.TaskID); err == nil {
			t.Fatalf("unrelated owner %q canceled a detached task", parent)
		}
	}
	for pass := 0; pass < 2; pass++ {
		result, err := next.await(ctx, "main", []string{task.TaskID}, nil)
		if err != nil || result.Status != "failed" || result.Error != "child failed" || result.AlreadyCollected != (pass > 0) {
			t.Fatalf("saved failed report pass %d: %+v %v", pass, result, err)
		}
	}
	if next.hasUncollectedRequired("main") {
		t.Fatal("detached failure blocked the next final answer")
	}
}
