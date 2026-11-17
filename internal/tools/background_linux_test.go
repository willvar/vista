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
	job, err := manager.Start(context.Background(), root, "run", root, "printf 'first🙂'; sleep 30", time.Minute, 0, nil)
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
	if _, err := manager.Start(context.Background(), root, "run", root, "printf output; sleep 30", time.Minute, 0, nil); err != nil {
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
	job, err := manager.Start(runCtx, filepath.Join(root, "state"), "run", root, "sleep 30 & echo $!; wait", 0, 0, nil)
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
			if event.TerminationReason != "stopped" || event.StartedAt == "" || event.ExpiresAt != "" || event.Signal == "" {
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
		{"success without deadline", "printf done", "succeeded", "", "", 0, false},
		{"long deadline", "printf done", "succeeded", "", "", 48 * time.Hour, false},
		{"nonzero", "printf error; exit 8", "failed", "exit", "", time.Minute, false},
		{"signal", "kill -TERM $$", "failed", "signal", "SIGTERM", time.Minute, false},
		{"timeout", "sleep 20", "interrupted", "timeout", "", 100 * time.Millisecond, false},
		{"worker close", "sleep 20", "interrupted", "worker_closed", "", time.Minute, true},
		{"worker close without deadline", "sleep 20", "interrupted", "worker_closed", "", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			events := make(chan BackgroundJob, 8)
			manager := NewBackgroundManager(func(_ context.Context, job BackgroundJob) error { events <- job; return nil })
			defer manager.Close()
			job, err := manager.Start(context.Background(), root, "run", root, tc.script, tc.timeout, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			started, startErr := time.Parse(time.RFC3339Nano, job.StartedAt)
			if startErr != nil {
				t.Fatalf("missing start time: %+v", job)
			}
			if tc.timeout == 0 {
				if job.ExpiresAt != "" {
					t.Fatalf("unexpected deadline: %+v", job)
				}
			} else {
				expires, expiryErr := time.Parse(time.RFC3339Nano, job.ExpiresAt)
				if expiryErr != nil || !expires.After(started) || expires.Sub(started) > tc.timeout {
					t.Fatalf("wrong deadline: %+v", job)
				}
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

func nextBackgroundEvent(t *testing.T, events <-chan BackgroundJob, match func(BackgroundJob) bool) BackgroundJob {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case job := <-events:
			if match(job) {
				return job
			}
		case <-deadline.C:
			t.Fatal("background event did not arrive")
			return BackgroundJob{}
		}
	}
}

func TestBackgroundWarningUpdateInvalidatesOldTimers(t *testing.T) {
	root := t.TempDir()
	events := make(chan BackgroundJob, 64)
	manager := NewBackgroundManager(func(_ context.Context, job BackgroundJob) error { events <- job; return nil })
	defer manager.Close()
	job, err := manager.Start(context.Background(), root, "run", root, "sleep 30", 800*time.Millisecond, 600*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	warned := nextBackgroundEvent(t, events, func(j BackgroundJob) bool { return j.Change == "warning" })
	if warned.Status != "running" || warned.WarnedAt == "" || warned.ExpiresAt != job.ExpiresAt {
		t.Fatalf("bad warning: %+v", warned)
	}
	updated, err := manager.UpdateTimeout(context.Background(), job.ID, job.ExpiresAt, 1800*time.Millisecond, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ExpiresAt == job.ExpiresAt || updated.WarnedAt != "" || updated.DeadlineRevision != job.DeadlineRevision+1 {
		t.Fatalf("bad deadline update: %+v", updated)
	}
	if _, err := manager.UpdateTimeout(context.Background(), job.ID, job.ExpiresAt, time.Minute, 0); err == nil {
		t.Fatal("stale update succeeded")
	}
	warning := nextBackgroundEvent(t, events, func(j BackgroundJob) bool { return j.Change == "warning" || j.Status == "interrupted" })
	if warning.Change != "warning" || warning.DeadlineRevision != updated.DeadlineRevision {
		t.Fatalf("old timer survived update: %+v", warning)
	}
	terminal := nextBackgroundEvent(t, events, func(j BackgroundJob) bool { return j.Status == "interrupted" })
	if terminal.TerminationReason != "timeout" || terminal.ExpiresAt != updated.ExpiresAt || terminal.Complete {
		t.Fatalf("wrong terminal: %+v", terminal)
	}
	if _, err := manager.UpdateTimeout(context.Background(), job.ID, updated.ExpiresAt, time.Minute, 0); err == nil {
		t.Fatal("terminated job was extended")
	}
}

func TestBackgroundFailedDeadlineUpdatePreservesOldHardLimit(t *testing.T) {
	root := t.TempDir()
	events := make(chan BackgroundJob, 32)
	manager := NewBackgroundManager(func(_ context.Context, job BackgroundJob) error {
		if job.Change == "deadline" {
			return errors.New("save failed")
		}
		events <- job
		return nil
	})
	defer manager.Close()
	job, err := manager.Start(context.Background(), root, "run", root, "sleep 30", 400*time.Millisecond, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpdateTimeout(context.Background(), job.ID, job.ExpiresAt, time.Minute, 0); err == nil {
		t.Fatal("failed commit succeeded")
	}
	terminal := nextBackgroundEvent(t, events, func(j BackgroundJob) bool { return j.Status == "interrupted" })
	if terminal.ExpiresAt != job.ExpiresAt || terminal.TerminationReason != "timeout" {
		t.Fatalf("failed update changed timer: %+v", terminal)
	}
}

func TestBackgroundHardDeadlineDoesNotWaitForWarningDelivery(t *testing.T) {
	root := t.TempDir()
	entered := make(chan struct{})
	release := make(chan struct{})
	events := make(chan BackgroundJob, 32)
	manager := NewBackgroundManager(func(_ context.Context, job BackgroundJob) error {
		if job.Change == "warning" {
			close(entered)
			<-release
		}
		events <- job
		return nil
	})
	defer manager.Close()
	job, err := manager.Start(context.Background(), root, "run", root, "echo $$; exec sleep 30", 600*time.Millisecond, 400*time.Millisecond, nil)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	<-entered
	data, err := os.ReadFile(job.OutputPath)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	time.Sleep(600 * time.Millisecond)
	status, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	close(release)
	if err == nil && !strings.Contains(string(status), "State:\tZ") {
		t.Fatalf("hard termination waited on warning delivery: %s", status)
	}
	terminal := nextBackgroundEvent(t, events, func(j BackgroundJob) bool { return j.Status == "interrupted" })
	if terminal.TerminationReason != "timeout" {
		t.Fatalf("wrong reason: %+v", terminal)
	}
}

func TestBackgroundLimitsAreLiveAndDoNotKillExistingJobs(t *testing.T) {
	root := t.TempDir()
	events := make(chan BackgroundJob, 64)
	manager := NewBackgroundManager(func(_ context.Context, job BackgroundJob) error { events <- job; return nil })
	defer manager.Close()
	if err := manager.SetLimit(2); err != nil {
		t.Fatal(err)
	}
	first, err := manager.Start(context.Background(), root, "run", root, "sleep 30", 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Start(context.Background(), root, "run", root, "sleep 30", 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetLimit(1); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), root, "run", root, "touch should-not-exist", 0, 0, nil); err == nil || !strings.Contains(err.Error(), "2 active / 1 allowed") {
		t.Fatalf("missing capacity error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "should-not-exist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected command executed")
	}
	manager.mu.Lock()
	active := len(manager.active)
	manager.mu.Unlock()
	if active != 2 {
		t.Fatal("lowering killed jobs")
	}
	if err := manager.SetLimit(3); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), root, "run", root, "sleep 30", 0, 0, nil); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{0, -1, MaxBackgroundJobs + 1} {
		if manager.SetLimit(limit) == nil {
			t.Fatal("invalid limit accepted")
		}
	}
	if first.ExpiresAt != "" || second.WarnAt != "" {
		t.Fatal("unlimited jobs acquired timers")
	}
}

func TestBackgroundWarningAfterSlowStartAcknowledgment(t *testing.T) {
	root := t.TempDir()
	events := make(chan BackgroundJob, 32)
	manager := NewBackgroundManager(func(_ context.Context, job BackgroundJob) error {
		if job.Status == "starting" {
			time.Sleep(250 * time.Millisecond)
		}
		events <- job
		return nil
	})
	defer manager.Close()
	job, err := manager.Start(context.Background(), root, "run", root, "sleep 30", time.Second, 900*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	warned := nextBackgroundEvent(t, events, func(j BackgroundJob) bool { return j.Change == "warning" || j.Status == "interrupted" })
	if warned.Change != "warning" || warned.ExpiresAt != job.ExpiresAt {
		t.Fatalf("startup lost warning or moved deadline: %+v", warned)
	}
}

func TestBackgroundHardDeadlineDoesNotWaitForRunningStateDelivery(t *testing.T) {
	root := t.TempDir()
	entered := make(chan BackgroundJob, 1)
	release := make(chan struct{})
	done := make(chan error, 1)
	manager := NewBackgroundManager(func(_ context.Context, job BackgroundJob) error {
		if job.Status == "running" {
			entered <- job
			<-release
		}
		return nil
	})
	defer manager.Close()
	go func() {
		_, err := manager.Start(context.Background(), root, "run", root, "echo $$; exec sleep 30", 500*time.Millisecond, 0, nil)
		done <- err
	}()
	job := <-entered
	time.Sleep(800 * time.Millisecond)
	data, err := os.ReadFile(job.OutputPath)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	status, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	close(release)
	if err == nil && !strings.Contains(string(status), "State:\tZ") {
		t.Fatalf("process survived while running-state delivery blocked: %s", status)
	}
	if err := <-done; err == nil {
		t.Fatal("deadline-expired start claimed success")
	}
}
