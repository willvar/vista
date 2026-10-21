package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"vista/internal/provider"
	"vista/internal/workerproto"
)

func TestDelegationCollectsReadyResultBeforeSlowTaskAndCanReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r := newDelegationRegistry()
	defer r.closeChildren("main")
	slowRelease := make(chan struct{})
	slow, err := r.start(ctx, "main", "slow", func(ctx context.Context, _ string) (string, *workerproto.OutputRef, error) {
		select {
		case <-slowRelease:
			return "slow evidence", &workerproto.OutputRef{RunID: "run", Turn: 2, Bytes: 13}, nil
		case <-ctx.Done():
			return "", nil, ctx.Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	fast, err := r.start(ctx, "main", "fast", func(context.Context, string) (string, *workerproto.OutputRef, error) {
		return "fast evidence", &workerproto.OutputRef{RunID: "run", Turn: 1, Bytes: 13}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{slow.TaskID, fast.TaskID}
	result, err := r.await(ctx, "main", ids, nil)
	if err != nil || result.TaskID != fast.TaskID || result.Answer != "fast evidence" || result.Report.Turn != 1 {
		t.Fatalf("first completion: %+v, %v", result, err)
	}
	zero := int64(0)
	result, err = r.await(ctx, "main", ids, &zero)
	if err != nil || result.Status != "running" || len(result.PendingTaskIDs) != 1 || result.PendingTaskIDs[0] != slow.TaskID {
		t.Fatalf("collected result hid pending task: %+v, %v", result, err)
	}
	close(slowRelease)
	result, err = r.await(ctx, "main", ids, nil)
	if err != nil || result.TaskID != slow.TaskID || result.Answer != "slow evidence" {
		t.Fatalf("second completion: %+v, %v", result, err)
	}
	// A compacted parent can retrieve the original report using only taskId.
	result, err = r.await(ctx, "main", []string{fast.TaskID}, nil)
	if err != nil || !result.AlreadyCollected || result.Answer != "fast evidence" || result.Report.Turn != 1 {
		t.Fatalf("report replay: %+v, %v", result, err)
	}
}

func TestDelegationAwaitRequiresExplicitIDs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r := newDelegationRegistry()
	defer r.closeChildren("main")
	first, err := r.start(ctx, "main", "first", func(context.Context, string) (string, *workerproto.OutputRef, error) {
		return "first evidence", &workerproto.OutputRef{RunID: "run", Turn: 1, Bytes: 13}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]string{nil, {}} {
		if _, err := r.await(ctx, "main", ids, nil); err == nil {
			t.Fatalf("await accepted missing task IDs: %#v", ids)
		}
	}
	result, err := r.await(ctx, "main", []string{first.TaskID}, nil)
	if err != nil || result.TaskID != first.TaskID || result.Answer != "first evidence" {
		t.Fatalf("explicit collection failed: %+v, %v", result, err)
	}
}

func TestDelegationFailureCancelOwnershipAndRepeatedIDs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r := newDelegationRegistry()
	defer r.closeChildren("main")
	failed, _ := r.start(ctx, "main", "same", func(context.Context, string) (string, *workerproto.OutputRef, error) {
		return "", nil, errors.New("model endpoint failed")
	})
	running, _ := r.start(ctx, "main", "same", func(ctx context.Context, _ string) (string, *workerproto.OutputRef, error) {
		<-ctx.Done()
		return "", nil, ctx.Err()
	})
	if failed.TaskID == running.TaskID {
		t.Fatal("repeated tool call ID overwrote a task")
	}
	if _, err := r.await(ctx, "sibling", []string{failed.TaskID}, nil); err == nil {
		t.Fatal("sibling collected another task's result")
	}
	if _, err := r.cancel(ctx, "sibling", running.TaskID); err == nil {
		t.Fatal("sibling canceled another task")
	}
	if _, err := r.await(ctx, "main", []string{"unknown"}, nil); err == nil {
		t.Fatal("unknown task accepted")
	}
	result, err := r.await(ctx, "main", []string{failed.TaskID}, nil)
	if err != nil || result.Status != "failed" || result.Error != "model endpoint failed" {
		t.Fatalf("child failure should be readable, not a parent error: %+v %v", result, err)
	}
	result, err = r.cancel(ctx, "main", running.TaskID)
	if err != nil || result.Status != "canceled" || result.Error == "" {
		t.Fatalf("cancellation was not collected: %+v %v", result, err)
	}
	for _, name := range []string{"delegate", "await_agent", "cancel_agent"} {
		call := provider.ToolCall{}
		call.Function.Name, call.Function.Arguments = name, `{}`
		if _, err := (&toolRuntime{}).execute(ctx, call); err == nil {
			t.Errorf("%s accepted missing runtime", name)
		}
	}
}

func TestPrematureFinalDoesNotInjectPendingNotice(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var parentTurns atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Messages[1].Content == "child" {
			continuationAnswer(w, "child evidence")
			return
		}
		switch parentTurns.Add(1) {
		case 1:
			continuationEvent(w, mcpToolEvent("child", "delegate", `{"task":"child"}`))
		case 2:
			continuationAnswer(w, "premature answer")
		case 3:
			last := request.Messages[len(request.Messages)-1]
			if last.Role != "system" || !strings.Contains(last.Content, `"type":"runtime_join"`) || strings.Contains(last.Content, "Runtime notice") {
				t.Errorf("coordinator join was not supplied as structured runtime context: %+v", last)
			}
			continuationAnswer(w, "final with evidence")
		default:
			t.Error("unexpected model request")
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var finalTurns []int32
	answer, err := Run(ctx, continuationCommand(server.URL, "main"), func(event workerproto.Result) error {
		if event.Kind == "model_complete" {
			finalTurns = append(finalTurns, event.Turn)
		}
		return nil
	})
	if err != nil || answer != "final with evidence" || len(finalTurns) != 1 || parentTurns.Load() != 3 {
		t.Fatalf("premature final: answer=%q final=%v turns=%d err=%v", answer, finalTurns, parentTurns.Load(), err)
	}
}

func TestPrematureFinalIsNotRetriedForPendingChildren(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var parentTurns atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if len(request.Messages) > 1 && request.Messages[1].Content == "child" {
			continuationAnswer(w, "best effort evidence")
			return
		}
		switch parentTurns.Add(1) {
		case 1:
			continuationEvent(w, mcpToolEvent("child", "delegate", `{"task":"child","join":"best_effort"}`))
		default:
			continuationAnswer(w, "same answer")
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	answer, err := Run(ctx, continuationCommand(server.URL, "main"), func(workerproto.Result) error { return nil })
	if err != nil || answer != "same answer" || parentTurns.Load() != 2 {
		t.Fatalf("pending child should not trigger another parent turn: answer=%q parentTurns=%d err=%v", answer, parentTurns.Load(), err)
	}
}

func TestRootCancellationJoinsChildBeforeReturning(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	childStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Messages[1].Content == "child" {
			close(childStarted)
			<-r.Context().Done()
			return
		}
		if len(request.Messages) == 2 {
			continuationEvent(w, mcpToolEvent("child", "delegate", `{"task":"child"}`))
			return
		}
		<-childStarted
		cancel()
		<-r.Context().Done()
	}))
	defer server.Close()
	var statuses []string
	_, err := Run(ctx, continuationCommand(server.URL, "main"), func(event workerproto.Result) error {
		if event.Kind == "agent_task" && event.Task.Status != "started" {
			statuses = append(statuses, event.Task.ID+":"+event.Task.Status)
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || len(statuses) != 2 || statuses[0] != "main/child:canceled" || statuses[1] != "main:canceled" {
		t.Fatalf("root returned before child cancellation: statuses=%v err=%v", statuses, err)
	}
}
