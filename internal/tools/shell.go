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

const maxShellOutput = 16 << 20
const maxShellPreview = 8 << 10

var ErrOutputLimit = errors.New("shell output exceeded 16 MiB")

type ShellResult struct {
	ArtifactID   string `json:"artifactId"`
	Directory    string `json:"directory"`
	OutputPath   string `json:"-"`
	Bytes        int64  `json:"bytes"`
	Preview      string `json:"preview"`
	PreviewBytes int    `json:"previewBytes"`
	Truncated    bool   `json:"truncated"`
	NextOffset   int64  `json:"nextOffset"`
	ExitCode     int    `json:"exitCode"`
	Signal       string `json:"signal,omitempty"`
	Complete     bool   `json:"complete"`
}

func (r ShellResult) ExitError() error {
	if r.Signal != "" {
		return fmt.Errorf("terminated by signal: %s", r.Signal)
	}
	if r.Complete && r.ExitCode != 0 {
		return fmt.Errorf("exit code %d", r.ExitCode)
	}
	return nil
}

type shellWriter struct {
	mu      sync.Mutex
	file    *os.File
	bytes   int64
	preview []byte
	failure error
	cancel  context.CancelFunc
	notify  chan<- struct{}
}

func (w *shellWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	if w.failure != nil {
		err := w.failure
		w.mu.Unlock()
		return 0, err
	}
	allowed := len(p)
	if int64(allowed) > maxShellOutput-w.bytes {
		allowed = int(maxShellOutput - w.bytes)
	}
	n, err := w.file.Write(p[:allowed])
	w.bytes += int64(n)
	if len(w.preview) < maxShellPreview {
		keep := min(n, maxShellPreview-len(w.preview))
		w.preview = append(w.preview, p[:keep]...)
	}
	if err == nil && n < allowed {
		err = errors.New("short shell output write")
	}
	if err == nil && allowed < len(p) {
		err = ErrOutputLimit
	}
	if err != nil {
		w.failure = err
	}
	w.mu.Unlock()
	if n > 0 && w.notify != nil {
		select {
		case w.notify <- struct{}{}:
		default:
		}
	}
	if err != nil {
		w.cancel()
	}
	return n, err
}

func RunShell(ctx context.Context, stateDir, directory, script string, timeout time.Duration) (ShellResult, error) {
	return RunShellRecorded(ctx, stateDir, directory, script, timeout, nil, nil)
}

func RunShellRecorded(ctx context.Context, stateDir, directory, script string, timeout time.Duration, started func(ShellResult) error, finished func(ShellResult, error) error) (result ShellResult, err error) {
	directory, err = absolute(directory)
	if err != nil {
		return ShellResult{}, err
	}
	if strings.TrimSpace(script) == "" || len(script) > 4096 {
		return ShellResult{}, errors.New("shell command must be 1–4096 bytes")
	}
	if timeout <= 0 || timeout > 5*time.Minute {
		return ShellResult{}, errors.New("shell timeout must be between 1 ns and 5 minutes")
	}
	info, err := os.Stat(directory)
	if err != nil {
		return ShellResult{}, err
	}
	if !info.IsDir() {
		return ShellResult{}, errors.New("shell directory is not a directory")
	}
	if stateDir == "" {
		return ShellResult{}, errors.New("state directory is required")
	}
	outputDir := filepath.Join(stateDir, "outputs")
	if err := os.MkdirAll(outputDir, 0700); err != nil {
		return ShellResult{}, err
	}
	file, err := os.CreateTemp(outputDir, "shell-*.log")
	if err != nil {
		return ShellResult{}, err
	}
	defer file.Close()
	result = ShellResult{ArtifactID: uuid.NewString(), Directory: directory, OutputPath: file.Name(), ExitCode: -1}
	defer func() {
		if finished != nil {
			if finishErr := finished(result, err); finishErr != nil {
				err = errors.Join(err, finishErr)
			}
		}
	}()
	if started != nil {
		if err := started(result); err != nil {
			return result, err
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := platform.ShellCommand(runCtx, script)
	cmd.Dir = directory
	cmd.WaitDelay = 2 * time.Second
	writer := &shellWriter{file: file, preview: make([]byte, 0, maxShellPreview), cancel: cancel}
	cmd.Stdout = writer
	cmd.Stderr = writer
	if err := platform.Start(cmd); err != nil {
		return result, err
	}
	processDone := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			_ = platform.KillProcessGroup(cmd.Process)
		case <-processDone:
		}
	}()
	waitErr := cmd.Wait()
	close(processDone)
	// A shell may have started descendants. This also closes inherited output pipes.
	_ = platform.KillProcessGroup(cmd.Process)
	if err := file.Sync(); err != nil && writer.failure == nil {
		writer.failure = err
	}
	result.Bytes = writer.bytes
	preview := completeUTF8Prefix(writer.preview)
	result.Preview = strings.ToValidUTF8(string(preview), "�")
	result.PreviewBytes = len(preview)
	result.NextOffset = int64(result.PreviewBytes)
	result.Truncated = result.NextOffset < result.Bytes
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
		result.Signal = platform.ProcessSignal(cmd.ProcessState)
	}
	if writer.failure != nil {
		return result, fmt.Errorf("%w; partial output saved as artifact %s (%d bytes)", writer.failure, result.ArtifactID, result.Bytes)
	}
	if err := runCtx.Err(); err != nil {
		return result, fmt.Errorf("shell stopped: %w; partial output saved as artifact %s (%d bytes)", err, result.ArtifactID, result.Bytes)
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			result.Complete = true
			return result, nil
		}
		return result, waitErr
	}
	result.Complete = true
	return result, nil
}
