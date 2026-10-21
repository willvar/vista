package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"vista/internal/agent"
	"vista/internal/appdirs"
	modelcfg "vista/internal/model"
	"vista/internal/platform"
	"vista/internal/provider"
	"vista/internal/resources"
	"vista/internal/store"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

// Run serves the worker IPC stream until its input closes.
func Run() error {
	configDir, err := appdirs.Config()
	if err != nil {
		return err
	}
	defer func() {
		if err := platform.KillSessionDescendants(os.Getpid()); err != nil && !errors.Is(err, platform.ErrUnsupported) {
			log.Printf("Worker descendant cleanup: %v", err)
		}
	}()
	terminated := make(chan os.Signal, 1)
	signal.Notify(terminated, syscall.SIGTERM)
	defer signal.Stop(terminated)
	signalDone := make(chan struct{})
	defer close(signalDone)
	go func() {
		select {
		case <-terminated:
			_ = platform.KillSessionDescendants(os.Getpid())
			os.Exit(1)
		case <-signalDone:
		}
	}()
	if _, err := fmt.Fprintln(os.Stdout, "READY"); err != nil {
		return err
	}
	input := json.NewDecoder(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	var outputMu sync.Mutex
	emit := func(result workerproto.Result) error {
		outputMu.Lock()
		defer outputMu.Unlock()
		return out.Encode(result)
	}
	var ackMu sync.Mutex
	pendingAcks := map[string]chan workerproto.Command{}
	awaitAck := func(ctx context.Context, result workerproto.Result) (workerproto.Command, error) {
		ackID := uuid.NewString()
		pending := make(chan workerproto.Command, 1)
		ackMu.Lock()
		pendingAcks[ackID] = pending
		ackMu.Unlock()
		result.AckID = ackID
		if err := emit(result); err != nil {
			ackMu.Lock()
			delete(pendingAcks, ackID)
			ackMu.Unlock()
			return workerproto.Command{}, err
		}
		select {
		case ack := <-pending:
			if ack.Error != "" {
				return workerproto.Command{}, errors.New(ack.Error)
			}
			return ack, ctx.Err()
		case <-ctx.Done():
			ackMu.Lock()
			delete(pendingAcks, ackID)
			ackMu.Unlock()
			return workerproto.Command{}, ctx.Err()
		}
	}
	emitPersisted := func(ctx context.Context, result workerproto.Result) error {
		_, err := awaitAck(ctx, result)
		return err
	}
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	defer cancelSession()
	sessionCtx = provider.WithCredentialResolver(sessionCtx, func(ctx context.Context, model modelcfg.Model) (modelcfg.Model, error) {
		request := &workerproto.CredentialRequest{Key: model.Key, Ref: model.CredentialRef}
		if deadline, ok := ctx.Deadline(); ok {
			request.Deadline = deadline.UnixMilli()
		}
		ack, err := awaitAck(ctx, workerproto.Result{Kind: "model_credentials", Credential: request})
		return ack.Model, err
	})
	agents := agent.NewSession(sessionCtx)
	jobs := tools.NewBackgroundManager(func(ctx context.Context, job tools.BackgroundJob) error {
		result := workerproto.Result{RunID: job.RunID, Kind: "job", Status: job.Status, TaskID: job.TaskID, ParentTaskID: job.ParentTaskID, Job: &workerproto.JobProgress{StartedAt: job.StartedAt, ExpiresAt: job.ExpiresAt, Signal: job.Signal, TerminationReason: job.TerminationReason, CallID: job.CallID, TaskID: job.TaskID, ParentTaskID: job.ParentTaskID, ID: job.ID, Directory: job.Directory, Command: job.Command, OutputPath: job.OutputPath, Bytes: job.Bytes, ExitCode: job.ExitCode, Complete: job.Complete, Error: job.Error}}
		if job.Status == "starting" {
			return emitPersisted(ctx, result)
		}
		return emit(result)
	})
	defer jobs.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			var memory runtime.MemStats
			runtime.ReadMemStats(&memory)
			_ = emit(workerproto.Result{Kind: "metrics", Metrics: &workerproto.Metrics{HeapBytes: memory.HeapAlloc, HeapObjects: memory.HeapObjects, HeapInuseBytes: memory.HeapInuse, HeapIdleBytes: memory.HeapIdle, HeapReleasedBytes: memory.HeapReleased, StackInuseBytes: memory.StackInuse, OtherSysBytes: memory.OtherSys, SysBytes: memory.Sys, GCCount: memory.NumGC, RSSBytes: platform.RSS(), PeakRSSBytes: platform.PeakRSS(), TotalAllocBytes: memory.TotalAlloc, Goroutines: runtime.NumGoroutine(), SampledAt: time.Now().UTC().Format(time.RFC3339)}})
			select {
			case <-done:
				return
			case <-ticker.C:
			}
		}
	}()
	var activeMu sync.Mutex
	activeID := ""
	var activeCancel context.CancelFunc
	activeDone := make(chan struct{})
	close(activeDone)
	for {
		var command workerproto.Command
		if err := input.Decode(&command); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("invalid worker command: %w", err)
		}
		if command.RunID == "" && command.Action != "ack" || len(command.Prompt) > workerproto.MaxPromptBytes {
			return errors.New("invalid worker command fields")
		}
		for _, image := range command.Images {
			if !resources.ValidHash(image.Hash) || image.Bytes <= 0 || image.Bytes > store.MaxImageBytes {
				return errors.New("invalid worker image size")
			}
		}
		for _, message := range command.History {
			for _, image := range message.Images {
				if !resources.ValidHash(image.Hash) || image.Bytes <= 0 || image.Bytes > store.MaxImageBytes {
					return errors.New("invalid worker history image size")
				}
			}
		}
		if command.Action == "ack" {
			if command.AckID == "" || len(command.AckID) > 64 {
				return errors.New("invalid worker acknowledgment")
			}
			ackMu.Lock()
			if pending := pendingAcks[command.AckID]; pending != nil {
				delete(pendingAcks, command.AckID)
				pending <- command
			}
			ackMu.Unlock()
			continue
		}
		if command.Action == "cancel" {
			activeMu.Lock()
			if activeID == command.RunID && activeCancel != nil {
				activeCancel()
			}
			activeMu.Unlock()
			continue
		}
		if command.Action == "stop_job" {
			if command.JobID == "" {
				return errors.New("missing background job ID")
			}
			_ = jobs.Stop(command.JobID)
			continue
		}
		if command.Action == "stop_agent" {
			if command.TaskID == "" || command.TaskID == "main" {
				return errors.New("missing delegated task ID")
			}
			agents.CancelTask(command.RunID, command.TaskID)
			continue
		}
		if command.Action != "" && command.Action != "run" && command.Action != "compact" {
			return errors.New("invalid worker action")
		}
		activeMu.Lock()
		if activeID != "" {
			previous := activeDone
			activeMu.Unlock()
			<-previous
			activeMu.Lock()
			if activeID != "" {
				activeMu.Unlock()
				return errors.New("worker received overlapping runs")
			}
		}
		runCtx, cancel := context.WithCancel(sessionCtx)
		activeID = command.RunID
		activeCancel = cancel
		activeDone = make(chan struct{})
		runDone := activeDone
		activeMu.Unlock()
		go func(command workerproto.Command) {
			defer close(runDone)
			defer cancel()
			if err := provider.LoadOMPCatalog(configDir); err != nil {
				log.Printf("Worker retaining current OMP catalog: %v", err)
			}
			var fullTurn int32
			var fullBytes int
			var pending *workerproto.PendingReply
			nextInput := func(reply *workerproto.CompletedReply) (*workerproto.Command, error) {
				ack, err := awaitAck(runCtx, workerproto.Result{RunID: command.RunID, Kind: "input_poll", Reply: reply})
				return ack.Input, err
			}
			emitForRun := func(result workerproto.Result) error {
				if result.Kind == "model_complete" {
					fullTurn, fullBytes = result.Turn, result.FullBytes
					return nil
				}
				if result.Kind == "question" {
					pending = result.Pending
					return nil
				}
				needsAck := result.Kind == "memory_change" || result.Kind == "checklist" || result.Kind == "context_notice" || result.Kind == "model_history" ||
					(result.Kind == "tool" || result.Kind == "artifact") && result.Status == "started" ||
					result.Kind == "agent_task"
				if !needsAck {
					return emit(result)
				}
				ackCtx := agents.TaskContext(result.RunID, result.TaskID, runCtx)
				if result.Kind == "model_history" || result.Kind == "agent_task" && result.Task != nil && result.Task.Status != "started" {
					// Canceled runs still commit completed model/tool history and
					// terminal reports while the Session's IPC connection is alive.
					if sessionCtx.Err() != nil {
						return emit(result)
					} // IPC input is closing; no ACK can arrive.
					ackCtx = sessionCtx
				}
				return emitPersisted(ackCtx, result)
			}
			result := workerproto.Result{RunID: command.RunID, Status: "succeeded"}
			switch {
			case command.Model.Provider == "":
				result.Status = "failed"
				result.Content = "No model is configured. Add a model to Vista's models.json and restart Vista. No task was performed."
			case provider.SupportsProtocol(command.Model.Provider):
				var content string
				var err error
				if command.Action == "compact" {
					err = agent.CompactSession(runCtx, command, emitForRun)
				} else {
					content, err = agents.Run(runCtx, command, emitForRun, nextInput, jobs)
				}
				if err != nil {
					if runCtx.Err() != nil {
						result.Status = "interrupted"
						result.Content = "Cancelled by user"
					} else {
						result.Status = "failed"
						result.Content = "Agent run failed: " + err.Error()
					}
				} else {
					result.Content = content
					result.Turn, result.FullBytes = fullTurn, fullBytes
					result.Pending = pending
				}
			default:
				result.Status = "failed"
				result.Content = "Unsupported model provider"
			}
			if runCtx.Err() != nil {
				result.Status = "interrupted"
				result.Content = "Cancelled by user"
			}
			if result.Status != "succeeded" {
				result.Turn, result.FullBytes = 0, 0
				result.Pending = nil
			}
			if command.Action == "compact" {
				result.Kind = "compaction_finished"
			}
			if err := emit(result); err != nil {
				log.Printf("Worker result write failed: %v", err)
			}
			activeMu.Lock()
			activeID = ""
			activeCancel = nil
			activeMu.Unlock()
		}(command)
	}
	cancelSession()
	activeMu.Lock()
	if activeCancel != nil {
		activeCancel()
	}
	finish := activeDone
	activeMu.Unlock()
	closed := make(chan struct{})
	go func() {
		<-finish
		agents.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		return errors.New("worker execution did not stop within five seconds")
	}
	return nil
}
