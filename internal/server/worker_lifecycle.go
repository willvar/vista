package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"vista/internal/modelcatalog"
	"vista/internal/platform"
	"vista/internal/store"
	"vista/internal/workerproto"
)

// A transition belongs to one session. Only its registration is protected by
// App.mu; slow process startup/teardown never blocks another session's commands.
type workerOperation struct {
	done   chan struct{}
	cancel context.CancelFunc
}

// Caller holds App.mu, which protects the registry and these lifecycle flags.
func workerCanReceive(w *worker) bool {
	return w != nil && !w.exiting && !w.stopping
}

func (a *App) workerOperation(ctx context.Context, id string, cancel context.CancelFunc) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		a.mu.Lock()
		if a.workerOperations == nil {
			a.workerOperations = make(map[string]*workerOperation)
		}
		previous := a.workerOperations[id]
		if previous == nil {
			op := &workerOperation{done: make(chan struct{}), cancel: cancel}
			a.workerOperations[id] = op
			a.mu.Unlock()
			return func() {
				a.mu.Lock()
				delete(a.workerOperations, id)
				close(op.done)
				a.mu.Unlock()
			}, nil
		}
		// A close or shutdown also cancels an unfinished start of this session.
		if cancel == nil && previous.cancel != nil {
			previous.cancel()
		}
		a.mu.Unlock()
		select {
		case <-previous.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (a *App) startWorker(id string) error {
	return a.startWorkerUsing(id, a.launchWorker)
}

func (a *App) startWorkerUsing(id string, launch func(context.Context, string) (*worker, *bufio.Scanner, error)) error {
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	release, err := a.workerOperation(ctx, id, cancel)
	if err != nil {
		return err
	}
	defer release()
	a.mu.Lock()
	previous := a.workers[id]
	if previous != nil && !previous.exiting {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()
	if previous != nil {
		select {
		case <-previous.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	available, err := platform.AvailableMemory()
	if err != nil {
		return fmt.Errorf("memory admission unavailable: %w", err)
	}
	a.mu.Lock()
	if a.workersStopping || ctx.Err() != nil {
		a.mu.Unlock()
		return errors.New("server is stopping")
	}
	if err := admitWorker(available, len(a.workers)+a.workerStarting); err != nil {
		a.mu.Unlock()
		return err
	}
	a.workerStarting++
	a.mu.Unlock()
	w, scanner, err := launch(ctx, id)
	a.mu.Lock()
	a.workerStarting--
	if err == nil && (a.workersStopping || ctx.Err() != nil) {
		err = context.Canceled
	}
	if err == nil {
		if a.workers == nil {
			a.workers = make(map[string]*worker)
		}
		a.workers[id] = w
	}
	a.mu.Unlock()
	if err != nil {
		if w != nil {
			a.discardWorker(w)
		}
		return err
	}
	go a.readWorkerOutput(id, w, scanner)
	return nil
}

func (a *App) launchWorker(ctx context.Context, id string) (*worker, *bufio.Scanner, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(exe, "worker")
	cmd.Env = modelcatalog.WorkerEnvironment(os.Environ(), a.modelCredentialEnvironment()...)
	return a.launchWorkerCommand(ctx, id, cmd)
}

func (a *App) launchWorkerCommand(ctx context.Context, id string, cmd *exec.Cmd) (_ *worker, _ *bufio.Scanner, err error) {
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, nil, err
	}
	logFile, err := os.OpenFile(filepath.Join(a.dir, "worker.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		input.Close()
		output.Close()
		return nil, nil, err
	}
	cmd.Stderr = logFile
	platform.AttachToParent(cmd)
	if err = platform.Start(cmd); err != nil {
		input.Close()
		output.Close()
		logFile.Close()
		return nil, nil, err
	}
	logFile.Close()
	w := &worker{cmd: cmd, input: input, done: make(chan struct{})}
	defer func() {
		if err != nil {
			a.discardWorker(w)
		}
	}()
	ready := make(chan bool, 1)
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), workerproto.MaxResultFrame)
	go func() {
		ready <- scanner.Scan() && scanner.Text() == "READY"
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case ok := <-ready:
		if !ok {
			return nil, nil, errors.New("worker failed before ready")
		}
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-timer.C:
		return nil, nil, errors.New("worker readiness timeout")
	}
	startTime, err := platform.WorkerSessionStart(cmd.Process.Pid)
	if err == nil {
		w.owner = store.WorkerOwner{SessionID: id, PID: cmd.Process.Pid, StartTime: startTime}
		err = a.store.SaveWorkerOwner(ctx, w.owner)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("record Worker ownership: %w", err)
	}
	return w, scanner, nil
}

// No result reader owns an unsuccessful start; reap it here before releasing
// the session transition, including its durable process ownership record.
func (a *App) discardWorker(w *worker) {
	_ = w.input.Close()
	_ = w.cmd.Process.Kill()
	if err := platform.KillSessionDescendants(w.cmd.Process.Pid); err != nil {
		log.Printf("worker startup descendant cleanup: %v", err)
	} else if w.owner.SessionID != "" {
		if err := a.store.ClearWorkerOwner(context.Background(), w.owner); err != nil {
			log.Printf("worker startup ownership cleanup: %v", err)
		}
	}
	_ = w.cmd.Wait()
}
func (a *App) recoverWorker(id string) {
	for attempt := 0; attempt < 3; attempt++ {
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(time.Duration(1<<attempt) * time.Second):
		}
		item, err := a.store.Get(a.ctx, id)
		if err != nil || !item.Open {
			return
		}
		if err := a.startWorkerUsing(id, a.launchOpenWorker); err == nil {
			log.Printf("worker %s recovered", id)
			go a.launchQueuedInput(id)
			return
		}
		log.Printf("worker %s restart %d failed: %v", id, attempt+1, err)
	}
}

func (a *App) launchOpenWorker(ctx context.Context, id string) (*worker, *bufio.Scanner, error) {
	// Recovery may have waited behind a close/delete operation after its first
	// session read. Recheck under this session's transition before creating a PID.
	item, err := a.store.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if !item.Open {
		return nil, nil, errors.New("session is closed")
	}
	return a.launchWorker(ctx, id)
}

func (a *App) stopWorker(id string) {
	release, _ := a.workerOperation(context.Background(), id, nil)
	defer release()
	a.mu.Lock()
	w := a.workers[id]
	if w != nil {
		w.stopping = true
		delete(a.workers, id)
		if a.workerFailures != nil {
			delete(a.workerFailures, id)
		}
	}
	a.mu.Unlock()
	if w != nil {
		_ = w.input.Close()
		select {
		case <-w.done:
		case <-time.After(3 * time.Second):
			if w.cmd.Process != nil {
				_ = platform.Terminate(w.cmd.Process)
			}
			select {
			case <-w.done:
			case <-time.After(2 * time.Second):
				_ = w.cmd.Process.Kill()
				<-w.done
			}
		}
		events, err := a.store.InterruptRunning(context.Background(), id)
		if err != nil {
			log.Printf("session %s interrupt record failed: %v", id, err)
		}
		for range events {
			a.hub.notify(id)
		}
		jobEvents, jobErr := a.store.InterruptJobs(context.Background(), id, "Worker stopped before the background job result was confirmed")
		if jobErr != nil {
			log.Printf("session %s job interrupt record failed: %v", id, jobErr)
		}
		for range jobEvents {
			a.hub.notify(id)
		}
		artifactEvents, artifactErr := a.store.InterruptArtifacts(context.Background(), id, "Worker stopped before output completeness was confirmed")
		if artifactErr != nil {
			log.Printf("session %s artifact interrupt record failed: %v", id, artifactErr)
		}
		for range artifactEvents {
			a.hub.notify(id)
		}
	}
}
func (a *App) stopAll() {
	a.mu.Lock()
	a.workersStopping = true
	ids := make(map[string]bool, len(a.workers)+len(a.workerOperations))
	for id := range a.workers {
		ids[id] = true
	}
	for id, op := range a.workerOperations {
		ids[id] = true
		if op.cancel != nil {
			op.cancel()
		}
	}
	a.mu.Unlock()
	for id := range ids {
		a.stopWorker(id)
	}
}
