package server

import (
	"bufio"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"vista/internal/workerproto"
)

type workerCommandSink struct{ commands chan string }

func (s *workerCommandSink) Write(data []byte) (int, error) {
	s.commands <- string(data)
	return len(data), nil
}
func (*workerCommandSink) Close() error { return nil }

func awaitWorkerTest[T any](t *testing.T, result <-chan T) T {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("worker lifecycle operation blocked")
		var zero T
		return zero
	}
}

func TestWorkerSlowStartupDoesNotBlockOtherSessionCommands(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &workerCommandSink{commands: make(chan string, 1)}
	ready := &worker{input: sink}
	app := &App{ctx: ctx, workers: map[string]*worker{"ready": ready}}
	entered, started := make(chan struct{}), make(chan error, 1)
	go func() {
		started <- app.startWorkerUsing("slow", func(ctx context.Context, _ string) (*worker, *bufio.Scanner, error) {
			close(entered)
			<-ctx.Done()
			return nil, nil, ctx.Err()
		})
	}()
	awaitWorkerTest(t, entered)
	sent := make(chan error, 1)
	go func() {
		app.recordWorkerMetrics("ready", ready, workerproto.Metrics{HeapBytes: 1024})
		sent <- app.sendWorker("ready", workerproto.Command{Action: "cancel", RunID: "active-run"})
	}()
	if err := awaitWorkerTest(t, sent); err != nil {
		t.Fatal(err)
	}
	awaitWorkerTest(t, sink.commands)
	stopped := make(chan struct{})
	go func() { app.stopWorker("slow"); close(stopped) }()
	awaitWorkerTest(t, stopped)
	if err := awaitWorkerTest(t, started); !errors.Is(err, context.Canceled) {
		t.Fatalf("pending start was not canceled: %v", err)
	}
	app.mu.Lock()
	defer app.mu.Unlock()
	if app.workerStarting != 0 || len(app.workerOperations) != 0 || app.workers["ready"] != ready {
		t.Fatal("startup failure left a reservation or changed another session")
	}
}

func TestWorkerShutdownCancelsIndependentPendingStartsAndRejectsNewStarts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app := &App{ctx: ctx}
	entered := make(chan string, 2)
	done := make(chan error, 3)
	var launches atomic.Int32
	launch := func(ctx context.Context, id string) (*worker, *bufio.Scanner, error) {
		launches.Add(1)
		entered <- id
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	go func() { done <- app.startWorkerUsing("first", launch) }()
	go func() { done <- app.startWorkerUsing("second", launch) }()
	a, b := awaitWorkerTest(t, entered), awaitWorkerTest(t, entered)
	if a == b {
		t.Fatal("independent startups were serialized or duplicated")
	}
	// A second caller for the same session waits for that session's transition.
	go func() { done <- app.startWorkerUsing("first", launch) }()
	stopped := make(chan struct{})
	go func() { app.stopAll(); close(stopped) }()
	awaitWorkerTest(t, stopped)
	for range 3 {
		if err := awaitWorkerTest(t, done); err == nil {
			t.Fatal("shutdown accepted a pending startup")
		}
	}
	if err := app.startWorkerUsing("after-shutdown", launch); err == nil {
		t.Fatal("startup accepted after shutdown")
	}
	if launches.Load() != 2 {
		t.Fatalf("duplicate session launched %d processes", launches.Load())
	}
	app.mu.Lock()
	defer app.mu.Unlock()
	if app.workerStarting != 0 || len(app.workerOperations) != 0 || len(app.workers) != 0 {
		t.Fatal("shutdown retained pending workers")
	}
}

func TestWorkerAcknowledgmentBelongsToOriginatingInstance(t *testing.T) {
	oldSink := &workerCommandSink{commands: make(chan string, 1)}
	newSink := &workerCommandSink{commands: make(chan string, 1)}
	oldWorker, newWorker := &worker{input: oldSink}, &worker{input: newSink}
	app := &App{ctx: context.Background(), workers: map[string]*worker{"same-session": oldWorker}}
	consumer := app.workerResultConsumer("same-session", oldWorker)
	app.workers["same-session"] = newWorker
	if err := consumer.send(workerproto.Command{Action: "ack", AckID: "old-call"}); err != nil {
		t.Fatal(err)
	}
	awaitWorkerTest(t, oldSink.commands)
	select {
	case <-newSink.commands:
		t.Fatal("old result sent an ACK to a replacement worker")
	default:
	}
}

func TestWorkerExitingInstanceRejectsNewCommands(t *testing.T) {
	sink := &workerCommandSink{commands: make(chan string, 1)}
	app := &App{workers: map[string]*worker{"session": {input: sink, exiting: true}}}
	if err := app.sendWorker("session", workerproto.Command{RunID: "new-run"}); err == nil {
		t.Fatal("new command dispatched to an exiting worker")
	}
	select {
	case <-sink.commands:
		t.Fatal("exiting worker received command")
	default:
	}
}

func TestWorkerRecoveryRechecksClosedSessionAfterWaiting(t *testing.T) {
	consumer, _ := newResultConsumerTest(t)
	app := &App{ctx: context.Background(), store: consumer.store, dir: consumer.stateDir}
	// The old recovery decision was made while this session was open.
	item, err := app.store.Get(app.ctx, "session")
	if err != nil || !item.Open {
		t.Fatalf("open session: %+v %v", item, err)
	}
	ctx, cancel := context.WithCancel(app.ctx)
	defer cancel()
	release, err := app.workerOperation(ctx, "session", cancel)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- app.startWorkerUsing("session", app.launchOpenWorker) }()
	if err := app.store.SetOpen(app.ctx, "session", false); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	if err := awaitWorkerTest(t, done); err == nil || !strings.Contains(err.Error(), "session is closed") {
		t.Fatalf("stale recovery started a closed session: %v", err)
	}
	if len(app.workers) != 0 {
		t.Fatal("closed session has a worker")
	}
}
