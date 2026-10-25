package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"vista/internal/platform"
)

const maxBackgroundJobs = 4

type BackgroundJob struct {
	CallID            string `json:"callId,omitempty"`
	TaskID            string `json:"taskId,omitempty"`
	ParentTaskID      string `json:"parentTaskId,omitempty"`
	ID                string `json:"id"`
	RunID             string `json:"runId"`
	Directory         string `json:"directory"`
	Command           string `json:"command"`
	OutputPath        string `json:"outputPath"`
	Status            string `json:"status"`
	Bytes             int64  `json:"bytes"`
	ExitCode          int    `json:"exitCode"`
	Complete          bool   `json:"complete"`
	Error             string `json:"error,omitempty"`
	StartedAt         string `json:"startedAt,omitempty"`
	ExpiresAt         string `json:"expiresAt,omitempty"`
	Signal            string `json:"signal,omitempty"`
	TerminationReason string `json:"terminationReason,omitempty"`
}

type backgroundProcess struct {
	cancel     context.CancelFunc
	done       chan struct{}
	stopReason string
}

// BackgroundManager belongs to one persistent Session Worker, not one run.
type BackgroundManager struct {
	mu     sync.Mutex
	active map[string]*backgroundProcess
	emit   func(context.Context, BackgroundJob) error
}

func NewBackgroundManager(emit func(context.Context, BackgroundJob) error) *BackgroundManager {
	return &BackgroundManager{active: make(map[string]*backgroundProcess), emit: emit}
}

func (m *BackgroundManager) Start(ctx context.Context, stateDir, runID, directory, script string, timeout time.Duration, secrets []string, identity ...string) (BackgroundJob, error) {
	directory, err := absolute(directory)
	if err != nil {
		return BackgroundJob{}, err
	}
	if strings.TrimSpace(script) == "" || len(script) > 4096 {
		return BackgroundJob{}, errors.New("background command must be 1–4096 bytes")
	}
	if timeout <= 0 || timeout > 24*time.Hour {
		return BackgroundJob{}, errors.New("background timeout must be within 24 hours")
	}
	info, err := os.Stat(directory)
	if err != nil {
		return BackgroundJob{}, err
	}
	if !info.IsDir() {
		return BackgroundJob{}, errors.New("background directory is not a directory")
	}
	if stateDir == "" || runID == "" {
		return BackgroundJob{}, errors.New("state directory and run ID are required")
	}
	outputDir := filepath.Join(stateDir, "outputs")
	if err := os.MkdirAll(outputDir, 0700); err != nil {
		return BackgroundJob{}, err
	}
	file, err := os.CreateTemp(outputDir, "job-*.log")
	if err != nil {
		return BackgroundJob{}, err
	}
	// Job is the display/persistence projection. Keep execution input in local
	// variables so no event, returned metadata, or later status bypasses redaction.
	job := BackgroundJob{ID: uuid.NewString(), RunID: runID, Directory: DisplayShellCommand(directory, secrets), Command: DisplayShellCommand(script, secrets), OutputPath: file.Name(), Status: "starting", ExitCode: -1}
	deadline := time.Now().Add(timeout)
	job.ExpiresAt = deadline.UTC().Format(time.RFC3339Nano)
	if len(identity) > 0 {
		job.CallID = identity[0]
	}
	if len(identity) > 1 {
		job.TaskID = identity[1]
	}
	if len(identity) > 2 {
		job.ParentTaskID = identity[2]
	}
	process := &backgroundProcess{done: make(chan struct{})}
	m.mu.Lock()
	if len(m.active) >= maxBackgroundJobs {
		m.mu.Unlock()
		file.Close()
		os.Remove(file.Name())
		return BackgroundJob{}, errors.New("session background job limit reached")
	}
	m.active[job.ID] = process
	m.mu.Unlock()
	release := func() {
		m.mu.Lock()
		delete(m.active, job.ID)
		m.mu.Unlock()
		close(process.done)
	}
	// The Server commits the starting row before a command with side effects runs.
	if err := m.emit(ctx, job); err != nil {
		job.Status, job.Error = "interrupted", "Background job start was not acknowledged"
		_ = m.emit(context.Background(), job)
		release()
		file.Close()
		return BackgroundJob{}, err
	}
	jobCtx, cancel := context.WithDeadline(context.Background(), deadline)
	m.mu.Lock()
	process.cancel = cancel
	if process.stopReason != "" {
		cancel()
	}
	m.mu.Unlock()
	cmd := platform.ShellCommand(jobCtx, script)
	cmd.Dir = directory
	cmd.WaitDelay = 2 * time.Second
	changed := make(chan struct{}, 1)
	writer := &shellWriter{file: file, preview: make([]byte, 0, maxShellPreview), cancel: cancel, notify: changed}
	cmd.Stdout, cmd.Stderr = writer, writer
	if err := ctx.Err(); err != nil {
		cancel()
		job.Status, job.Error = "interrupted", err.Error()
		_ = m.emit(context.Background(), job)
		release()
		file.Close()
		return job, err
	}
	if err := jobCtx.Err(); err != nil {
		cancel()
		job.Status, job.Error = "interrupted", err.Error()
		_ = m.emit(context.Background(), job)
		release()
		file.Close()
		return job, err
	}
	if err := platform.Start(cmd); err != nil {
		cancel()
		job.Status, job.Error = "failed", err.Error()
		_ = m.emit(context.Background(), job)
		release()
		file.Close()
		return job, err
	}
	job.Status = "running"
	job.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := m.emit(ctx, job); err != nil {
		cancel()
		_ = platform.KillProcessGroup(cmd.Process)
		_ = cmd.Wait()
		job.Status, job.Error = "interrupted", "Background job running state was not confirmed"
		_ = m.emit(context.Background(), job)
		file.Close()
		release()
		return job, err
	}
	go func(job BackgroundJob) {
		defer release()
		defer cancel()
		defer file.Close()
		finished := make(chan struct{})
		progressDone := make(chan struct{})
		var progressErr error
		go func() {
			defer close(progressDone)
			for {
				select {
				case <-finished:
					return
				case <-changed:
				}
				// Writes trigger bounded metadata updates; an idle process does
				// not generate progress traffic or require browser polling.
				timer := time.NewTimer(time.Second)
				select {
				case <-finished:
					timer.Stop()
					return
				case <-timer.C:
				}
				select {
				case <-changed:
				default:
				}
				writer.mu.Lock()
				progress := job
				progress.Bytes = writer.bytes
				writer.mu.Unlock()
				if err := m.emit(jobCtx, progress); err != nil {
					if jobCtx.Err() == nil {
						progressErr = fmt.Errorf("record background output progress: %w", err)
						cancel()
					}
					return
				}
			}
		}()
		go func() {
			select {
			case <-jobCtx.Done():
				_ = platform.KillProcessGroup(cmd.Process)
			case <-finished:
			}
		}()
		waitErr := cmd.Wait()
		close(finished)
		<-progressDone
		_ = platform.KillProcessGroup(cmd.Process)
		if syncErr := file.Sync(); syncErr != nil && writer.failure == nil {
			writer.failure = syncErr
		}
		job.Bytes = writer.bytes
		if cmd.ProcessState != nil {
			job.ExitCode = cmd.ProcessState.ExitCode()
			job.Signal = platform.ProcessSignal(cmd.ProcessState)
		}
		switch {
		case writer.failure != nil:
			job.Status, job.Error = "failed", writer.failure.Error()
			job.TerminationReason = "output_error"
		case progressErr != nil:
			job.Status, job.Error = "failed", progressErr.Error()
			job.TerminationReason = "output_error"
		case jobCtx.Err() != nil:
			job.Status, job.Error = "interrupted", jobCtx.Err().Error()
			m.mu.Lock()
			job.TerminationReason = process.stopReason
			m.mu.Unlock()
			if job.TerminationReason == "" {
				job.TerminationReason = "timeout"
			}
		case waitErr != nil:
			var exit *exec.ExitError
			if errors.As(waitErr, &exit) {
				job.Status, job.Complete = "failed", true
				job.Error = fmt.Sprintf("exit code %d", job.ExitCode)
				job.TerminationReason = "exit"
				if job.Signal != "" {
					job.TerminationReason, job.Error = "signal", "terminated by signal: "+job.Signal
				}
			} else {
				job.Status, job.Error = "failed", waitErr.Error()
			}
		default:
			job.Status, job.Complete = "succeeded", true
		}
		_ = m.emit(context.Background(), job)
	}(job)
	return job, nil
}

func (m *BackgroundManager) Stop(id string) error {
	m.mu.Lock()
	process := m.active[id]
	if process == nil {
		m.mu.Unlock()
		return errors.New("background job is not active")
	}
	if process.stopReason == "" {
		process.stopReason = "stopped"
	}
	cancel := process.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (m *BackgroundManager) Close() {
	m.mu.Lock()
	processes := make([]*backgroundProcess, 0, len(m.active))
	for _, process := range m.active {
		if process.stopReason == "" {
			process.stopReason = "worker_closed"
		}
		processes = append(processes, process)
	}
	m.mu.Unlock()
	for _, process := range processes {
		m.mu.Lock()
		cancel := process.cancel
		m.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
	deadline := time.After(5 * time.Second)
	for _, process := range processes {
		select {
		case <-process.done:
		case <-deadline:
			return
		}
	}
}
