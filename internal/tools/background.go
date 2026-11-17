package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"vista/internal/platform"
	"vista/internal/toolinput"
)

const DefaultBackgroundJobs = 4
const MaxBackgroundJobs = 1024

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
	WarnAt            string `json:"warnAt,omitempty"`
	WarnedAt          string `json:"warnedAt,omitempty"`
	DeadlineRevision  uint64 `json:"deadlineRevision,omitempty"`
	Signal            string `json:"signal,omitempty"`
	TerminationReason string `json:"terminationReason,omitempty"`
	Change            string `json:"-"`
}

type backgroundProcess struct {
	// Serialize metadata writes, never hard termination, across output, warnings,
	// deadline updates and exit. The manager mutex owns the remaining fields.
	events     sync.Mutex
	job        BackgroundJob
	cancel     context.CancelFunc
	done       chan struct{}
	deadline   time.Time
	warnBefore time.Duration
	timer      *time.Timer
	warning    *time.Timer
	terminal   bool
	stopReason string
}

// BackgroundManager belongs to one persistent Session Worker, not one run.
type BackgroundManager struct {
	mu     sync.Mutex
	active map[string]*backgroundProcess
	limit  int
	emit   func(context.Context, BackgroundJob) error
}

func NewBackgroundManager(emit func(context.Context, BackgroundJob) error) *BackgroundManager {
	return &BackgroundManager{active: make(map[string]*backgroundProcess), limit: DefaultBackgroundJobs, emit: emit}
}

func (m *BackgroundManager) SetLimit(limit int) error {
	if limit < 1 || limit > MaxBackgroundJobs {
		return fmt.Errorf("background job limit must be between 1 and %d", MaxBackgroundJobs)
	}
	m.mu.Lock()
	m.limit = limit
	m.mu.Unlock()
	return nil
}

func validateBackgroundTiming(timeout, warnBefore time.Duration) error {
	if timeout < 0 {
		return errors.New("background timeout must not be negative")
	}
	if warnBefore < 0 || warnBefore > 0 && (timeout == 0 || warnBefore >= timeout) {
		return errors.New("warnBeforeSeconds requires a timeout and must be positive and less than timeoutSeconds")
	}
	return nil
}

func setBackgroundTiming(job *BackgroundJob, now time.Time, timeout, warnBefore time.Duration) time.Time {
	job.ExpiresAt, job.WarnAt, job.WarnedAt = "", "", ""
	job.DeadlineRevision++
	if timeout == 0 {
		return time.Time{}
	}
	deadline := now.Add(timeout)
	job.ExpiresAt = deadline.UTC().Format(time.RFC3339Nano)
	if warnBefore > 0 {
		job.WarnAt = deadline.Add(-warnBefore).UTC().Format(time.RFC3339Nano)
	}
	return deadline
}

// Caller holds m.mu. Timer callbacks check the schedule identity under the same
// mutex: stopping a Timer alone cannot invalidate a callback already dispatched.
func (m *BackgroundManager) scheduleLocked(p *backgroundProcess) {
	if p.timer != nil {
		p.timer.Stop()
	}
	if p.warning != nil {
		p.warning.Stop()
	}
	if p.deadline.IsZero() {
		return
	}
	revision := p.job.DeadlineRevision
	p.timer = time.AfterFunc(time.Until(p.deadline), func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if p.terminal || p.stopReason != "" || p.job.DeadlineRevision != revision {
			return
		}
		p.stopReason = "timeout"
		p.cancel()
	})
	if p.job.WarnAt != "" {
		// Derive from the monotonic deadline, not its wall-clock display string.
		p.warning = time.AfterFunc(time.Until(p.deadline.Add(-p.warnBefore)), func() { m.warn(p, revision) })
	}
}

func (m *BackgroundManager) warn(p *backgroundProcess, revision uint64) {
	p.events.Lock()
	defer p.events.Unlock()
	m.mu.Lock()
	if p.terminal || p.stopReason != "" || p.job.Status != "running" || p.job.DeadlineRevision != revision || p.job.WarnedAt != "" || !time.Now().Before(p.deadline) {
		m.mu.Unlock()
		return
	}
	p.job.WarnedAt = time.Now().UTC().Format(time.RFC3339Nano)
	job := p.job
	job.Change = "warning"
	m.mu.Unlock()
	// Delivery and inference may be unavailable; neither postpones the timer.
	_ = m.emit(context.Background(), job)
}

func (m *BackgroundManager) Start(ctx context.Context, stateDir, runID, directory, script string, timeout, warnBefore time.Duration, secrets []string, identity ...string) (BackgroundJob, error) {
	directory, err := absolute(directory)
	if err != nil {
		return BackgroundJob{}, err
	}
	if err := toolinput.CheckText("command", script, toolinput.MaxArgumentsBytes, "Reduce the complete JSON arguments; run an existing script file when the source is longer than the tool argument budget."); err != nil {
		return BackgroundJob{}, err
	}
	if err := validateBackgroundTiming(timeout, warnBefore); err != nil {
		return BackgroundJob{}, err
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
	// Execution input stays local; events contain only the redacted projection.
	job := BackgroundJob{ID: uuid.NewString(), RunID: runID, Directory: ShellCommandPreview(directory, secrets), Command: ShellCommandPreview(script, secrets), OutputPath: file.Name(), Status: "starting", ExitCode: -1}
	deadline := setBackgroundTiming(&job, time.Now(), timeout, warnBefore)
	if len(identity) > 0 {
		job.CallID = identity[0]
	}
	if len(identity) > 1 {
		job.TaskID = identity[1]
	}
	if len(identity) > 2 {
		job.ParentTaskID = identity[2]
	}
	jobCtx, cancel := context.WithCancel(context.Background())
	process := &backgroundProcess{done: make(chan struct{}), job: job, cancel: cancel, deadline: deadline, warnBefore: warnBefore}
	m.mu.Lock()
	if len(m.active) >= m.limit {
		active, limit := len(m.active), m.limit
		m.mu.Unlock()
		cancel()
		file.Close()
		os.Remove(file.Name())
		return BackgroundJob{}, fmt.Errorf("session background job limit reached: %d active / %d allowed", active, limit)
	}
	m.active[job.ID] = process
	m.scheduleLocked(process)
	m.mu.Unlock()
	cleanupScript := func() {}
	release := func() {
		cleanupScript()
		cancel()
		m.mu.Lock()
		process.terminal = true
		if process.timer != nil {
			process.timer.Stop()
		}
		if process.warning != nil {
			process.warning.Stop()
		}
		delete(m.active, job.ID)
		m.mu.Unlock()
		close(process.done)
	}
	fail := func(status, message string) {
		m.mu.Lock()
		job.TerminationReason = process.stopReason
		m.mu.Unlock()
		if job.TerminationReason == "timeout" {
			status, message = "interrupted", context.DeadlineExceeded.Error()
		}
		job.Status, job.Error = status, message
		_ = m.emit(context.Background(), job)
		release()
		file.Close()
	}
	// Commit the starting row before running a command with side effects.
	if err := m.emit(ctx, job); err != nil {
		fail("interrupted", "Background job start was not acknowledged")
		return BackgroundJob{}, err
	}
	cmd, cleanup, err := platform.ShellCommand(jobCtx, stateDir, script)
	if err != nil {
		fail("failed", err.Error())
		return job, err
	}
	cleanupScript = cleanup
	cmd.Dir = directory
	cmd.WaitDelay = 2 * time.Second
	changed := make(chan struct{}, 1)
	writer := &shellWriter{file: file, preview: make([]byte, 0, maxShellPreview), cancel: cancel, notify: changed}
	cmd.Stdout, cmd.Stderr = writer, writer
	if err := ctx.Err(); err != nil {
		fail("interrupted", err.Error())
		return job, err
	}
	if err := jobCtx.Err(); err != nil {
		fail("interrupted", err.Error())
		return job, err
	}
	if err := platform.Start(cmd); err != nil {
		fail("failed", err.Error())
		return job, err
	}
	finished := make(chan struct{})
	go func() {
		select {
		case <-jobCtx.Done():
			_ = platform.KillProcessGroup(cmd.Process)
		case <-finished:
		}
	}()
	job.Status = "running"
	job.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	process.events.Lock()
	m.mu.Lock()
	process.job = job
	// A slow startup acknowledgment may have crossed the warning time.
	// Re-arm against the same absolute deadline, never granting more time.
	m.scheduleLocked(process)
	m.mu.Unlock()
	err = m.emit(ctx, job)
	process.events.Unlock()
	if err == nil {
		err = jobCtx.Err()
	}
	if err != nil {
		cancel()
		_ = platform.KillProcessGroup(cmd.Process)
		_ = cmd.Wait()
		close(finished)
		fail("interrupted", "Background job running state was not confirmed")
		return job, err
	}
	go func() {
		defer release()
		defer file.Close()
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
				process.events.Lock()
				m.mu.Lock()
				progress, terminal := process.job, process.terminal
				m.mu.Unlock()
				writer.mu.Lock()
				progress.Bytes = writer.bytes
				writer.mu.Unlock()
				m.mu.Lock()
				process.job.Bytes = progress.Bytes
				m.mu.Unlock()
				var err error
				if !terminal {
					err = m.emit(jobCtx, progress)
				}
				process.events.Unlock()
				if err != nil {
					if jobCtx.Err() == nil {
						progressErr = fmt.Errorf("record background output progress: %w", err)
						cancel()
					}
					return
				}
			}
		}()
		waitErr := cmd.Wait()
		m.mu.Lock()
		process.terminal = true
		if process.timer != nil {
			process.timer.Stop()
		}
		if process.warning != nil {
			process.warning.Stop()
		}
		m.mu.Unlock()
		close(finished)
		<-progressDone
		_ = platform.KillProcessGroup(cmd.Process)
		if syncErr := file.Sync(); syncErr != nil && writer.failure == nil {
			writer.failure = syncErr
		}
		process.events.Lock()
		defer process.events.Unlock()
		m.mu.Lock()
		job := process.job
		reason := process.stopReason
		m.mu.Unlock()
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
			job.TerminationReason = reason
			if reason == "timeout" {
				job.Error = context.DeadlineExceeded.Error()
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
	}()
	return job, nil
}

// UpdateTimeout commits a candidate while the old hard timer remains armed.
// A lost/failed acknowledgment never grants more execution time. A timer or exit
// winning that race makes the update fail; the subsequent terminal event wins.
func (m *BackgroundManager) UpdateTimeout(ctx context.Context, id, expectedExpiresAt string, timeout, warnBefore time.Duration) (BackgroundJob, error) {
	if timeout <= 0 {
		return BackgroundJob{}, errors.New("timeoutSeconds must be positive")
	}
	if err := validateBackgroundTiming(timeout, warnBefore); err != nil {
		return BackgroundJob{}, err
	}
	m.mu.Lock()
	p := m.active[id]
	m.mu.Unlock()
	if p == nil {
		return BackgroundJob{}, errors.New("background job is not active")
	}
	p.events.Lock()
	defer p.events.Unlock()
	m.mu.Lock()
	if p.terminal || p.stopReason != "" || p.job.Status != "running" || p.job.ExpiresAt != expectedExpiresAt || !p.deadline.IsZero() && !time.Now().Before(p.deadline) {
		m.mu.Unlock()
		return BackgroundJob{}, errors.New("background job stopped or deadline changed; inspect its current state before retrying")
	}
	previous := p.job
	candidate := previous
	deadline := setBackgroundTiming(&candidate, time.Now(), timeout, warnBefore)
	candidate.Change = "deadline"
	m.mu.Unlock()
	rollback := func() {
		previous.Change = "deadline"
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = m.emit(rollbackCtx, previous)
	}
	if err := m.emit(ctx, candidate); err != nil {
		rollback()
		return BackgroundJob{}, err
	}
	m.mu.Lock()
	if p.terminal || p.stopReason != "" || !time.Now().Before(deadline) || ctx.Err() != nil {
		m.mu.Unlock()
		rollback()
		return BackgroundJob{}, errors.New("background job stopped before deadline update was confirmed")
	}
	defer m.mu.Unlock()
	candidate.Change = ""
	p.job, p.deadline, p.warnBefore = candidate, deadline, warnBefore
	m.scheduleLocked(p)
	return candidate, nil
}

func (m *BackgroundManager) Stop(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.active[id]
	if p == nil || p.terminal {
		return errors.New("background job is not active")
	}
	if p.stopReason == "" {
		p.stopReason = "stopped"
	}
	p.cancel()
	return nil
}

func (m *BackgroundManager) Close() {
	m.mu.Lock()
	processes := make([]*backgroundProcess, 0, len(m.active))
	for _, p := range m.active {
		if p.stopReason == "" {
			p.stopReason = "worker_closed"
		}
		p.cancel()
		processes = append(processes, p)
	}
	m.mu.Unlock()
	deadline := time.After(5 * time.Second)
	for _, p := range processes {
		select {
		case <-p.done:
		case <-deadline:
			return
		}
	}
}
