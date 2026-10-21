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

	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestDetachedTaskSurvivesParentAndNextRun(t *testing.T) {
	for _, ending := range []string{"success", "failure", "cancel"} {
		t.Run(ending, func(t *testing.T) {
			db, baseCtx := checkpointFixture(t)
			ctx, cancel := context.WithTimeout(baseCtx, 5*time.Second)
			defer cancel()
			parentCtx, cancelParent := context.WithCancel(ctx)
			defer cancelParent()
			childStarted, release := make(chan struct{}), make(chan struct{})
			var turns, nextTurns, collectTurns atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct{ Messages []workerproto.Message }
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				latestUser := ""
				for _, message := range request.Messages {
					if message.Role == "user" {
						latestUser = message.Content
					}
				}
				switch latestUser {
				case "child":
					close(childStarted)
					select {
					case <-release:
						continuationAnswer(w, "background report")
					case <-r.Context().Done():
					}
				case "next":
					if nextTurns.Add(1) == 1 {
						var restored bool
						for _, message := range request.Messages {
							restored = restored || message.Role == "tool" && strings.Contains(message.Content, `"taskId":"main/child"`)
						}
						if !restored {
							t.Error("detached task ID was absent from the restored model history")
						}
						continuationEvent(w, mcpToolEvent("inspect-old", "await_agent", `{"taskIds":["main/child"],"timeoutSeconds":0}`))
						return
					}
					last := request.Messages[len(request.Messages)-1]
					if last.Role != "tool" || !strings.Contains(last.Content, `"pendingTaskIds":["main/child"]`) || strings.Contains(last.Content, `"error"`) {
						t.Errorf("next run cannot inspect live detached task: %+v", last)
					}
					continuationAnswer(w, "next reply")
				case "collect", "replay":
					if collectTurns.Add(1)%2 == 1 {
						continuationEvent(w, mcpToolEvent("collect-old", "await_agent", `{"taskIds":["main/child"]}`))
						return
					}
					last := request.Messages[len(request.Messages)-1]
					var result delegationResult
					if err := json.Unmarshal([]byte(last.Content), &result); err != nil || result.Status != "succeeded" || result.Answer != "background report" || result.Report == nil || result.Report.RunID != "continuation-run" || result.AlreadyCollected != (latestUser == "replay") {
						t.Errorf("next run lost saved detached report: %+v %v", result, err)
					}
					continuationAnswer(w, "report read")
				default:
					if turns.Add(1) == 1 {
						continuationEvent(w, mcpToolEvent("child", "delegate", `{"task":"child","join":"detached"}`))
						return
					}
					select {
					case <-childStarted:
					case <-ctx.Done():
						return
					}
					switch ending {
					case "failure":
						http.Error(w, "failed", 400)
					case "cancel":
						cancelParent()
						<-r.Context().Done()
					default:
						continuationAnswer(w, "parent reply")
					}
				}
			}))
			defer server.Close()
			session := NewSession(ctx)
			defer session.Close()
			completed := make(chan workerproto.AgentTask, 1)
			var finalMessages atomic.Int32
			var final store.ModelOutputRef
			sink := historyTestSink(db, workerproto.Command{SessionID: "session"}, &final)
			emit := func(result workerproto.Result) error {
				if result.Kind == "model_complete" {
					finalMessages.Add(1)
				}
				if result.Task != nil && result.Task.ID == "main/child" && result.Task.Status != "started" {
					completed <- *result.Task
				}
				if result.Kind == "agent_task" {
					_, err := db.RecordAgentTask(ctx, "session", result)
					return err
				}
				return sink(result)
			}
			begin := func(runID, prompt string) workerproto.Command {
				user, _, err := db.BeginRun(ctx, "session", runID, runID+"-user", prompt)
				if err != nil {
					t.Fatal(err)
				}
				command := continuationCommand(server.URL, prompt)
				command.RunID, command.SessionID, command.MessageSequence = runID, "session", user.Sequence
				return command
			}
			command := begin("continuation-run", "main")
			answer, err := session.Run(parentCtx, command, emit, nil, nil)
			if (err == nil) != (ending == "success") {
				t.Fatalf("parent ending %s: %v", ending, err)
			}
			status := map[string]string{"success": "succeeded", "failure": "failed", "cancel": "interrupted"}[ending]
			var outputs []store.ModelOutputRef
			if err == nil {
				outputs = append(outputs, final)
			} else {
				answer = "foreground ended"
			}
			if _, err := db.FinishRun(ctx, "session", command.RunID, command.RunID+"-answer", status, answer, outputs...); err != nil {
				t.Fatal(err)
			}
			cancelParent()
			childCtx := session.TaskContext(command.RunID, "main/child", parentCtx)
			if childCtx.Err() != nil {
				t.Fatalf("detached task died with parent: %v", childCtx.Err())
			}
			next := begin("next-run", "next")
			answer, err = session.Run(ctx, next, emit, nil, nil)
			if err != nil || answer != "next reply" {
				t.Fatalf("next run: %q %v", answer, err)
			}
			if _, err := db.FinishRun(ctx, "session", next.RunID, next.RunID+"-answer", "succeeded", answer, final); err != nil {
				t.Fatal(err)
			}
			before := finalMessages.Load()
			close(release)
			select {
			case task := <-completed:
				if task.Status != "succeeded" || task.Report == nil || task.Report.RunID != command.RunID {
					t.Fatalf("lost report: %+v", task)
				}
			case <-ctx.Done():
				t.Fatal("background did not finish")
			}
			if finalMessages.Load() != before {
				t.Fatal("detached completion emitted another formal reply")
			}
			for _, prompt := range []string{"collect", "replay"} {
				next := begin(prompt+"-run", prompt)
				answer, err := session.Run(ctx, next, emit, nil, nil)
				if err != nil || answer != "report read" {
					t.Fatalf("%s saved detached report: %q %v", prompt, answer, err)
				}
				if _, err := db.FinishRun(ctx, "session", next.RunID, next.RunID+"-answer", "succeeded", answer, final); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestExplicitCancellationIncludesDetachedDescendants(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	registry := newDelegationRegistry()
	registry.sessionCtx = ctx
	defer registry.close()
	r := registry.forRun("first")
	started := make(chan string, 1)
	parent, err := r.startWithJoin(ctx, "main", "parent", "detached", func(parentCtx context.Context, parentID string) (string, *workerproto.OutputRef, error) {
		child, err := r.startWithJoin(parentCtx, parentID, "nested", "detached", func(childCtx context.Context, _ string) (string, *workerproto.OutputRef, error) {
			<-childCtx.Done()
			return "", nil, childCtx.Err()
		})
		if err != nil {
			return "", nil, err
		}
		started <- child.TaskID
		<-parentCtx.Done()
		return "", nil, parentCtx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	var childID string
	select {
	case childID = <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	result, err := registry.forRun("next").cancel(ctx, "main", parent.TaskID)
	if err != nil || result.Status != "canceled" {
		t.Fatalf("cancel parent: %+v %v", result, err)
	}
	child, err := r.await(ctx, parent.TaskID, []string{childID}, nil)
	if err != nil || child.Status != "canceled" {
		t.Fatalf("cancel descendant: %+v %v", child, err)
	}
	if _, err := r.startWithJoin(r.tasks[childID].ctx, childID, "late", "detached", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled subtree spawned work: %v", err)
	}
}

func TestSessionCloseCancelsDetachedTasks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	session := NewSession(ctx)
	r := newDelegationRegistry()
	r.sessionCtx = session.ctx
	session.runs["run"] = &runScope{delegations: r}
	task, err := r.startWithJoin(ctx, "main", "background", "detached", func(ctx context.Context, _ string) (string, *workerproto.OutputRef, error) {
		<-ctx.Done()
		return "", nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	session.Close()
	result, err := r.await(ctx, "main", []string{task.TaskID}, nil)
	if err != nil || result.Status != "canceled" {
		t.Fatalf("session close: %+v %v", result, err)
	}
}

func TestCancellationDuringTaskRegistrationStillPublishesTerminalState(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var statuses []string
	_, err := Run(ctx, workerproto.Command{RunID: "run", Prompt: "work"}, func(result workerproto.Result) error {
		if result.Task == nil {
			t.Fatalf("work started after canceled registration: %+v", result)
		}
		statuses = append(statuses, result.Task.Status)
		if result.Task.Status == "started" {
			cancel()
			return ctx.Err()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || len(statuses) != 2 || statuses[0] != "started" || statuses[1] != "canceled" {
		t.Fatalf("orphaned task: %v %v", statuses, err)
	}
}
