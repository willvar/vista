package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"vista/internal/instance"
)

func TestDesktopStatusPresentation(t *testing.T) {
	var status desktopStatus
	err := json.Unmarshal([]byte(`{
		"state":"running", "version":"dev", "binaryHash":"abcdef0123456789",
		"sourceCommit":"123456789abcdef", "sourceModified":true, "hostOS":"Windows 11 · 10.0.26300",
		"address":"http://127.0.0.1:6186", "uptimeSeconds":90061,
		"provider":"模型连接", "model":"fixture-model", "defaultModel":"Fixture / fixture-model", "defaultModelKey":"fixture/fixture-model",
		"sessions":{"total":5,"open":3,"closed":2,"running":1,"idle":2,"unavailable":1,"workers":2},
		"managedBackgroundProcesses":4,"eventStreams":2,"eventStreamLimit":64,
		"memory":{"heapBytes":1048576,"rssBytes":3145728,"peakRssBytes":4194304},
		"workerMetrics":[
			{"sessionId":"z-session","pid":456,"memory":{}},
			{"sessionId":"a-session","pid":123,"memory":{"heapBytes":2097152,"rssBytes":5242880,"peakRssBytes":6291456,"goroutines":7,"sampledAt":"2026-10-22T03:04:05Z"}}
		], "sampledAt":"2026-10-22T03:04:06Z"
	}`), &status)
	if err != nil {
		t.Fatal(err)
	}
	text := status.text()
	for _, want := range []string{
		"State: running", "Address: http://127.0.0.1:6186", "Uptime: 1d 1h1m1s",
		"Open: 3", "Closed: 2", "Running: 1", "Workers: 2", "Background processes: 4", "Event streams: 2 / 64",
		"Provider: 模型连接", "Model: fixture-model", "Resident: 3.0 MiB", "Go heap: 1.0 MiB",
		"Binary SHA-256: abcdef0123456789", "Source commit: 123456789abcdef (modified)",
		"Host OS: Windows 11 · 10.0.26300", "Conversation: a-session", "Goroutines: 7", "Memory sample not yet available.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("status missing %q:\n%s", want, text)
		}
	}
	if strings.Index(text, "Worker 123") > strings.Index(text, "Worker 456") {
		t.Fatal("Worker reports are not stable")
	}
	if strings.Contains(strings.ReplaceAll(text, "\r\n", ""), "\n") {
		t.Fatal("native control needs CRLF line endings")
	}
	status.Model = ""
	if text = status.text(); !strings.Contains(text, "No model connected.") || strings.Contains(text, "Model: fixture-model") {
		t.Fatal(text)
	}
}

func TestDesktopStatusRequestErrorsAndCancellation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	t.Setenv("HOME", root)
	d, err := dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(d, 0700); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"invalid JSON", "HTTP failure", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.WriteHeader(200)
					return
				} // instance probe
				switch mode {
				case "invalid JSON":
					_, _ = w.Write([]byte("{"))
				case "HTTP failure":
					http.Error(w, "unavailable", 503)
				case "timeout":
					<-r.Context().Done()
				}
			}))
			defer endpoint.Close()
			if err := instance.Write(d, instance.Info{PID: os.Getpid(), Address: endpoint.URL}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if text, err := loadDesktopStatus(ctx); err == nil {
				t.Fatalf("%s became a success: %q", mode, text)
			}
		})
	}
	_ = os.Remove(d + string(os.PathSeparator) + "instance.json")
	text, err := loadDesktopStatus(context.Background())
	if err != nil || text != "Vista is stopped." {
		t.Fatalf("stopped: %q %v", text, err)
	}
}
