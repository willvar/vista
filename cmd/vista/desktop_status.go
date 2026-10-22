package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"vista/internal/workerproto"
)

// This is a presentation of /api/status, not a second source of runtime state.
type desktopStatus struct {
	State, Version, BinaryHash, SourceCommit, HostOS, Address  string
	Provider, Model, DefaultModel, DefaultModelKey             string
	SourceModified                                             bool
	UptimeSeconds                                              int64
	Sessions                                                   struct{ Total, Open, Closed, Running, Idle, Unavailable, Workers int }
	ManagedBackgroundProcesses, EventStreams, EventStreamLimit int
	Memory                                                     struct{ HeapBytes, RSSBytes, PeakRSSBytes uint64 }
	WorkerMetrics                                              []struct {
		SessionID string
		PID       int
		Memory    workerproto.Metrics
	}
	SampledAt string
}

func loadDesktopStatus(ctx context.Context) (string, error) {
	resp, err := requestContext(ctx, http.MethodGet, "/api/status")
	if errors.Is(err, os.ErrNotExist) {
		return "Vista is stopped.", nil
	}
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status failed: HTTP %d", resp.StatusCode)
	}
	var status desktopStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return "", fmt.Errorf("read status: %w", err)
	}
	return status.text(), nil
}

func (s desktopStatus) text() string {
	var b strings.Builder
	section := func(title string) { fmt.Fprintf(&b, "\n%s\n", title) }
	line := func(label string, value any) { fmt.Fprintf(&b, "%s: %v\n", label, value) }
	fallback := func(value string) string {
		if value == "" {
			return "Not available"
		}
		return value
	}
	fmt.Fprintln(&b, "VISTA STATUS")
	line("Sampled", statusTime(s.SampledAt))
	section("Service")
	line("State", fallback(s.State))
	line("Address", fallback(s.Address))
	line("Uptime", statusUptime(s.UptimeSeconds))
	section("Conversations and processes")
	line("Conversations", s.Sessions.Total)
	line("Open", s.Sessions.Open)
	line("Closed", s.Sessions.Closed)
	line("Running", s.Sessions.Running)
	line("Idle", s.Sessions.Idle)
	line("Unavailable", s.Sessions.Unavailable)
	line("Workers", s.Sessions.Workers)
	line("Background processes", s.ManagedBackgroundProcesses)
	line("Event streams", fmt.Sprintf("%d / %d", s.EventStreams, s.EventStreamLimit))
	section("Model")
	if s.Model == "" {
		fmt.Fprintln(&b, "No model connected.")
	} else {
		line("Provider", fallback(s.Provider))
		line("Model", s.Model)
		line("Default selection", fallback(s.DefaultModel))
		line("Selection key", fallback(s.DefaultModelKey))
	}
	section("Server memory")
	line("Resident", statusBytes(s.Memory.RSSBytes))
	line("Peak resident", statusBytes(s.Memory.PeakRSSBytes))
	line("Go heap", statusBytes(s.Memory.HeapBytes))
	section("Build and host")
	line("Version", fallback(s.Version))
	line("Binary SHA-256", fallback(s.BinaryHash))
	commit := fallback(s.SourceCommit)
	if s.SourceModified {
		commit += " (modified)"
	}
	line("Source commit", commit)
	line("Host OS", fallback(s.HostOS))
	// API map iteration order is unstable. Keep process reports easy to compare.
	sort.Slice(s.WorkerMetrics, func(i, j int) bool { return s.WorkerMetrics[i].SessionID < s.WorkerMetrics[j].SessionID })
	for _, worker := range s.WorkerMetrics {
		section(fmt.Sprintf("Worker %d", worker.PID))
		line("Conversation", worker.SessionID)
		m := worker.Memory
		if m.SampledAt == "" {
			fmt.Fprintln(&b, "Memory sample not yet available.")
			continue
		}
		line("Sampled", statusTime(m.SampledAt))
		line("Resident", statusBytes(m.RSSBytes))
		line("Peak resident", statusBytes(m.PeakRSSBytes))
		line("Go heap", statusBytes(m.HeapBytes))
		line("Heap objects", m.HeapObjects)
		line("Heap in use", statusBytes(m.HeapInuseBytes))
		line("Heap idle", statusBytes(m.HeapIdleBytes))
		line("Heap released", statusBytes(m.HeapReleasedBytes))
		line("Stack in use", statusBytes(m.StackInuseBytes))
		line("Other Go memory", statusBytes(m.OtherSysBytes))
		line("Go reserved memory", statusBytes(m.SysBytes))
		line("Total allocated", statusBytes(m.TotalAllocBytes))
		line("Garbage collections", m.GCCount)
		line("Goroutines", m.Goroutines)
	}
	return strings.ReplaceAll(b.String(), "\n", "\r\n")
}

func statusBytes(value uint64) string {
	if value < 1024 {
		return fmt.Sprintf("%d B", value)
	}
	n, unit := float64(value), 0
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	for n >= 1024 && unit < len(units)-1 {
		n /= 1024
		unit++
	}
	return fmt.Sprintf("%.1f %s", n, units[unit])
}

func statusUptime(seconds int64) string {
	seconds = max(0, seconds)
	days := seconds / 86400
	tail := (time.Duration(seconds%86400) * time.Second).String()
	if days > 0 {
		return fmt.Sprintf("%dd %s", days, tail)
	}
	return tail
}

func statusTime(value string) string {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.Local().Format("2006-01-02 15:04:05 MST")
	}
	if value == "" {
		return "Not available"
	}
	return value
}
