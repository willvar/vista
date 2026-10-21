package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"vista/internal/agentconfig"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func newResultConsumerTest(t *testing.T) (*workerResultConsumer, int64) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Create(ctx, "session", "worker results"); err != nil {
		t.Fatal(err)
	}
	_, event, err := db.BeginRun(ctx, "session", "run", "user", "work")
	if err != nil {
		t.Fatal(err)
	}
	return &workerResultConsumer{
		ctx: ctx, store: db, sessionID: "session", stateDir: dir,
		notify: func() {}, send: func(workerproto.Command) error { return nil },
		finished: func() {},
	}, event.Sequence
}

func consumeTestResult(t *testing.T, consumer *workerResultConsumer, result workerproto.Result) error {
	t.Helper()
	frame, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return consumer.consumeFrame(frame)
}

func TestWorkerResultConsumerRejectsMalformedFramesWithoutEffects(t *testing.T) {
	for name, frame := range map[string]string{
		"invalid JSON":     "{",
		"unknown kind":     `{"runId":"run","kind":"surprise"}`,
		"invalid depth":    fmt.Sprintf(`{"runId":"run","depth":%d,"status":"succeeded"}`, agentconfig.MaxDelegationDepth+1),
		"missing model":    `{"runId":"run","kind":"model_result","turn":1}`,
		"invalid usage":    `{"runId":"run","kind":"model_result","turn":1,"modelResult":{"provider":"openai-chat","id":"model","usage":{"inputTokens":-1}}}`,
		"missing tool ACK": `{"runId":"run","kind":"tool","status":"started","tool":{"callId":"call","name":"shell"}}`,
		"invalid status":   `{"runId":"run","status":"running"}`,
		"invalid reply":    `{"runId":"run","kind":"input_poll","ackId":"poll","reply":{"turn":0,"fullBytes":5,"content":"hello"}}`,
		"child reply":      `{"runId":"run","kind":"input_poll","ackId":"poll","depth":1,"reply":{"turn":1,"fullBytes":5,"content":"hello"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			consumer, sequence := newResultConsumerTest(t)
			consumer.notify = func() { t.Fatal("invalid frame notified readers") }
			consumer.send = func(workerproto.Command) error { t.Fatal("invalid frame acknowledged"); return nil }
			consumer.finished = func() { t.Fatal("invalid frame launched next input") }
			if err := consumer.consumeFrame([]byte(frame)); err == nil {
				t.Fatal("invalid result was accepted")
			}
			events, err := consumer.store.EventsAfter(consumer.ctx, consumer.sessionID, sequence, 100)
			if err != nil || len(events) != 0 {
				t.Fatalf("invalid result persisted: events=%+v error=%v", events, err)
			}
			if runID, err := consumer.store.RunningRunID(consumer.ctx, consumer.sessionID); err != nil || runID != "run" {
				t.Fatalf("invalid result changed execution status: run=%s error=%v", runID, err)
			}
		})
	}
}

func TestWorkerResultConsumerToolAcknowledgmentRequiresCommit(t *testing.T) {
	for _, scenario := range []string{"active", "inactive", "storage failure", "ACK failure"} {
		t.Run(scenario, func(t *testing.T) {
			consumer, sequence := newResultConsumerTest(t)
			if scenario == "inactive" {
				if _, err := consumer.store.FinishRun(consumer.ctx, consumer.sessionID, "run", "answer", "succeeded", "done"); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "storage failure" {
				if err := consumer.store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			var notified, acknowledged int
			consumer.notify = func() { notified++ }
			consumer.send = func(command workerproto.Command) error {
				acknowledged++
				if notified != 1 || command.Action != "ack" || command.RunID != "run" || command.AckID != "ack-tool" {
					t.Fatalf("incorrect acknowledgment or notification order: notified=%d command=%+v", notified, command)
				}
				events, err := consumer.store.EventsAfter(consumer.ctx, consumer.sessionID, sequence, 100)
				if err != nil || len(events) != 1 || events[0].Kind != "tool" {
					t.Fatalf("tool was acknowledged before commit: events=%+v error=%v", events, err)
				}
				var result workerproto.Result
				if err := json.Unmarshal(events[0].Data, &result); err != nil || result.AckID != "" || result.Status != "started" || result.Tool == nil || result.Tool.CallID != "call" {
					t.Fatalf("incorrect persisted tool event: %+v error=%v", result, err)
				}
				if scenario == "ACK failure" {
					return errors.New("pipe closed")
				}
				return nil
			}
			err := consumeTestResult(t, consumer, workerproto.Result{RunID: "run", Kind: "tool", Status: "started", AckID: "ack-tool", Tool: &workerproto.ToolProgress{CallID: "call", Name: "shell"}})
			if (err == nil) != (scenario == "active") {
				t.Fatalf("scenario %s: unexpected error %v", scenario, err)
			}
			want := 0
			if scenario == "active" || scenario == "ACK failure" {
				want = 1
			}
			if acknowledged != want || notified != want {
				t.Fatalf("notified=%d acknowledged=%d want=%d", notified, acknowledged, want)
			}
		})
	}
}

func TestWorkerResultConsumerCompletionUsesCommittedStatus(t *testing.T) {
	for _, scenario := range []struct {
		name, status, wantStatus string
		unfinishedTool, launch   bool
	}{
		{"succeeded", "succeeded", "succeeded", false, true},
		{"failed", "failed", "failed", false, false},
		{"interrupted", "interrupted", "interrupted", false, false},
		{"unconfirmed tool", "succeeded", "failed", true, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			consumer, sequence := newResultConsumerTest(t)
			if scenario.unfinishedTool {
				if err := consumeTestResult(t, consumer, workerproto.Result{RunID: "run", Kind: "tool", Status: "started", AckID: "ack-tool", Tool: &workerproto.ToolProgress{CallID: "call", Name: "shell"}}); err != nil {
					t.Fatal(err)
				}
			}
			var notified, launched int
			consumer.notify = func() { notified++ }
			consumer.finished = func() {
				launched++
				if notified != 1 {
					t.Fatal("queued input launched before completion notification")
				}
				if _, err := consumer.store.RunningRunID(consumer.ctx, consumer.sessionID); !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("queued input launched before completion committed: %v", err)
				}
			}
			result := workerproto.Result{RunID: "run", Status: scenario.status, Content: "finished"}
			if err := consumeTestResult(t, consumer, result); err != nil {
				t.Fatal(err)
			}
			events, err := consumer.store.EventsAfter(consumer.ctx, consumer.sessionID, sequence, 100)
			if err != nil || len(events) == 0 {
				t.Fatalf("missing completion: events=%+v error=%v", events, err)
			}
			var completion *store.Event
			for index := range events {
				if events[index].Kind == "run_finished" {
					completion = &events[index]
				}
			}
			if completion == nil {
				t.Fatal("missing run_finished event")
			}
			var finished struct {
				Status       string `json:"status"`
				UnknownTools int    `json:"unknownTools"`
			}
			if err := json.Unmarshal(completion.Data, &finished); err != nil || finished.Status != scenario.wantStatus || (finished.UnknownTools != 0) != scenario.unfinishedTool {
				t.Fatalf("incorrect completion: data=%+v error=%v", finished, err)
			}
			wantLaunch := 0
			if scenario.launch {
				wantLaunch = 1
			}
			if notified != 1 || launched != wantLaunch {
				t.Fatalf("notified=%d launched=%d wantLaunch=%t", notified, launched, scenario.launch)
			}
			// A duplicate terminal frame cannot notify or dispatch again.
			if err := consumeTestResult(t, consumer, result); err != nil {
				t.Fatal(err)
			}
			if notified != 1 || launched != wantLaunch {
				t.Fatalf("duplicate result repeated completion effects: notified=%d launched=%d", notified, launched)
			}
		})
	}
}

func TestWorkerResultConsumerInputPollReportsDeliveryFailure(t *testing.T) {
	for _, sendFailed := range []bool{false, true} {
		t.Run(fmt.Sprint(sendFailed), func(t *testing.T) {
			consumer := &workerResultConsumer{
				pollInput: func(runID string, _ *workerproto.CompletedReply) (*workerproto.Command, error) {
					if runID != "run" {
						t.Fatalf("incorrect poll run %s", runID)
					}
					return nil, errors.New("storage unavailable")
				},
			}
			called := false
			consumer.send = func(ack workerproto.Command) error {
				called = true
				if ack.Action != "ack" || ack.AckID != "poll" || ack.RunID != "run" || ack.Input != nil || ack.Error != "Could not deliver pending input: storage unavailable" {
					t.Fatalf("incorrect failure acknowledgment: %+v", ack)
				}
				if sendFailed {
					return errors.New("pipe closed")
				}
				return nil
			}
			err := consumeTestResult(t, consumer, workerproto.Result{Kind: "input_poll", RunID: "run", AckID: "poll"})
			if !called || (err != nil) != sendFailed {
				t.Fatalf("delivery failure handling: called=%t error=%v", called, err)
			}
		})
	}
}

func TestWorkerMetricsIgnoreReplacedWorker(t *testing.T) {
	old, current := &worker{}, &worker{}
	app := &App{workers: map[string]*worker{"session": current}}
	metrics := workerproto.Metrics{HeapBytes: ^uint64(0), RSSBytes: ^uint64(0)}
	app.recordWorkerMetrics("session", old, metrics)
	if old.overBudget || current.overBudget || old.metrics != (workerproto.Metrics{}) || current.metrics != (workerproto.Metrics{}) {
		t.Fatal("retired worker metrics changed replacement state")
	}
}
