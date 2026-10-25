//go:build linux

package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBackgroundOutputPublishesGrowthBeforeExit(t *testing.T) {
	root := t.TempDir()
	events := make(chan BackgroundJob, 32)
	manager := NewBackgroundManager(func(_ context.Context, job BackgroundJob) error { events <- job; return nil })
	defer manager.Close()
	job, err := manager.Start(context.Background(), root, "run", root, "printf 'first🙂'; sleep 30", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case progress := <-events:
			if progress.Status != "running" || progress.Bytes == 0 {
				continue
			}
			if progress.ID != job.ID || progress.Bytes != int64(len("first🙂")) || progress.Complete {
				t.Fatalf("invalid progress: %+v", progress)
			}
			if data, err := os.ReadFile(job.OutputPath); err != nil || string(data) != "first🙂" {
				t.Fatalf("growth announced before output was readable: %q %v", data, err)
			}
			return
		case <-deadline:
			t.Fatal("no output notification while process was still running")
		}
	}
}

func TestBackgroundProgressFailureIsNotReportedAsTimeout(t *testing.T) {
	root := t.TempDir()
	events := make(chan BackgroundJob, 32)
	manager := NewBackgroundManager(func(_ context.Context, job BackgroundJob) error {
		events <- job
		if job.Status == "running" && job.Bytes > 0 {
			return errors.New("progress storage unavailable")
		}
		return nil
	})
	defer manager.Close()
	if _, err := manager.Start(context.Background(), root, "run", root, "printf output; sleep 30", time.Minute, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(4 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Status == "starting" || event.Status == "running" {
				continue
			}
			if event.Status != "failed" || event.TerminationReason != "output_error" || !strings.Contains(event.Error, "progress storage unavailable") {
				t.Fatalf("incorrect progress failure: %+v", event)
			}
			manager.Close()
			select {
			case late := <-events:
				t.Fatalf("event after terminal state: %+v", late)
			default:
			}
			return
		case <-deadline:
			t.Fatal("progress failure did not stop the background job")
		}
	}
}

func TestBackgroundJobOutlivesRunAndStopsDescendants(t *testing.T) {
	root := t.TempDir()
	events := make(chan BackgroundJob, 8)
	manager := NewBackgroundManager(func(_ context.Context, job BackgroundJob) error {
		events <- job
		return nil
	})
	defer manager.Close()
	runCtx, cancelRun := context.WithCancel(context.Background())
	job, err := manager.Start(runCtx, filepath.Join(root, "state"), "run", root, "sleep 30 & echo $!; wait", time.Minute, nil)
	if err != nil || job.Status != "running" {
		t.Fatalf("background start: %+v %v", job, err)
	}
	cancelRun()
	var pid int
	for i := 0; i < 100; i++ {
		data, readErr := os.ReadFile(job.OutputPath)
		if readErr == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
		if pid > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("background child did not start")
	}
	if err := manager.Stop(job.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(4 * time.Second)
	for {
		select {
		case event := <-events:
			if event.ID != job.ID || event.Status != "interrupted" {
				continue
			}
			if event.Complete {
				t.Fatal("stopped job reported complete")
			}
			if event.TerminationReason != "stopped" || event.StartedAt == "" || event.ExpiresAt == "" || event.Signal == "" {
				t.Fatalf("stop metadata lost: %+v", event)
			}
			for i := 0; i < 100; i++ {
				status, readErr := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
				if errors.Is(readErr, os.ErrNotExist) || (readErr == nil && strings.Contains(string(status), "State:\tZ")) {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatalf("child %d survived job stop: %v", pid, syscall.Kill(pid, 0))
		case <-deadline:
			t.Fatal("job stop did not finish")
		}
	}
}

func TestBackgroundTerminalReasons(t *testing.T) {
	for _, tc := range []struct {
		name, script, status, reason, signal string
		timeout                              time.Duration
		close                                bool
	}{
		{"success", "printf done", "succeeded", "", "", time.Minute, false},
		{"nonzero", "printf error; exit 8", "failed", "exit", "", time.Minute, false},
		{"signal", "kill -TERM $$", "failed", "signal", "SIGTERM", time.Minute, false},
		{"timeout", "sleep 20", "interrupted", "timeout", "", 100 * time.Millisecond, false},
		{"worker close", "sleep 20", "interrupted", "worker_closed", "", time.Minute, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			events := make(chan BackgroundJob, 8)
			manager := NewBackgroundManager(func(_ context.Context, job BackgroundJob) error { events <- job; return nil })
			defer manager.Close()
			job, err := manager.Start(context.Background(), root, "run", root, tc.script, tc.timeout, nil)
			if err != nil {
				t.Fatal(err)
			}
			started, startErr := time.Parse(time.RFC3339Nano, job.StartedAt)
			expires, expiryErr := time.Parse(time.RFC3339Nano, job.ExpiresAt)
			if startErr != nil || expiryErr != nil || !expires.After(started) {
				t.Fatalf("missing lifetime: %+v", job)
			}
			if tc.close {
				manager.Close()
			}
			for {
				select {
				case event := <-events:
					if event.Status == "starting" || event.Status == "running" {
						continue
					}
					if event.Status != tc.status || event.TerminationReason != tc.reason || tc.signal != "" && event.Signal != tc.signal || event.ExpiresAt != job.ExpiresAt || event.StartedAt != job.StartedAt {
						t.Fatalf("wrong terminal cause: %+v", event)
					}
					return
				case <-time.After(4 * time.Second):
					t.Fatal("job did not terminate")
				}
			}
		})
	}
}
