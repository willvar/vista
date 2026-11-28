package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"vista/internal/instance"
)

func waitDesktop(t *testing.T, what string, check func() bool) {
	t.Helper()
	for until := time.Now().Add(25 * time.Second); time.Now().Before(until); time.Sleep(100 * time.Millisecond) {
		if check() {
			return
		}
	}
	t.Fatalf("timed out: %s", what)
}

func TestNativeLinuxTray(t *testing.T) {
	binary := os.Getenv("VISTA_TEST_DESKTOP")
	if binary == "" {
		t.Skip("VISTA_TEST_DESKTOP must point at an unpacked native archive")
	}
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Fatal("test requires an isolated Wayland desktop with a tray panel")
	}
	for _, mode := range []string{"desktop-first", "web-first", "tray-at-launch"} {
		t.Run(mode, func(t *testing.T) { testNativeLinuxTray(t, binary, mode) })
	}
}

func testNativeLinuxTray(t *testing.T, binary, mode string) {
	bus, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("VISTA_HOST", "127.0.0.1")
	t.Setenv("VISTA_NO_BROWSER", "1")
	config := filepath.Join(root, "vista")
	if err := os.Mkdir(config, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "model-catalog-status.json"), []byte(`{"nextCheckAt":"2099-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	t.Setenv("VISTA_PORT", strconv.Itoa(port))
	base, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d/", port))
	t.Cleanup(func() {
		if _, err := os.Stat(filepath.Join(config, "state", "instance.json")); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			if out, err := exec.CommandContext(ctx, binary, "stop").CombinedOutput(); err != nil {
				t.Errorf("cleanup isolated service: %s: %v", out, err)
			}
		}
	})
	browserURL := filepath.Join(root, "browser-url")
	// Observe the handoff without opening a browser on the developer's display.
	if mode != "tray-at-launch" {
		if err := os.WriteFile(filepath.Join(root, "xdg-open"), []byte(`#!/bin/sh
printf '%s\n' "$1" > "$VISTA_TEST_BROWSER_URL"
`), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	openWeb := func() serviceStatus {
		t.Helper()
		if err := os.Remove(browserURL); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "web")
		cmd.Env = append(os.Environ(), "VISTA_NO_BROWSER=0", "VISTA_TEST_BROWSER_URL="+browserURL)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("web entry point: %s: %v", out, err)
		}
		waitDesktop(t, "web hands the expected URL to the browser", func() bool {
			data, e := os.ReadFile(browserURL)
			return e == nil && strings.TrimSpace(string(data)) == base.String()
		})
		s, err := readStatus(base)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	var firstService serviceStatus
	if mode == "web-first" {
		firstService = openWeb()
		if firstService.EventStreams != 0 {
			t.Fatal("web started a desktop window")
		}
	}
	logPath := filepath.Join(root, "desktop.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		log.Close()
		if t.Failed() {
			data, _ := os.ReadFile(logPath)
			t.Logf("desktop output: %s", data)
		}
	})
	var args []string
	if mode == "tray-at-launch" {
		args = []string{"--tray"}
	}
	cmd := exec.Command(binary, args...)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var exitErr error
	go func() { exitErr = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = cmd.Process.Kill()
			<-done
		}
	})
	itemList := func() []string {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return linuxTrayItems(ctx, bus, uint32(cmd.Process.Pid))
	}
	waitDesktop(t, "the packaged desktop registers one item", func() bool { return len(itemList()) == 1 })
	first := itemList()
	menu := bus.Object(strings.SplitN(first[0], "/", 2)[0], "/StatusNotifierMenu")
	type menuEntry struct {
		ID         int32
		Properties map[string]dbus.Variant
		Children   []dbus.Variant
	}
	readMenu := func() map[string]int32 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var revision uint32
		var top menuEntry
		if err := menu.CallWithContext(ctx, "com.canonical.dbusmenu.GetLayout", 0, int32(0), int32(1), []string{"label"}).Store(&revision, &top); err != nil {
			t.Fatal(err)
		}
		labels := map[string]int32{}
		for _, variant := range top.Children {
			var entry menuEntry
			if err := dbus.Store([]any{variant.Value()}, &entry); err != nil {
				t.Fatal(err)
			}
			if label, ok := entry.Properties["label"]; ok {
				if s, ok := label.Value().(string); ok {
					labels[s] = entry.ID
				}
			}
		}
		return labels
	}
	click := func(label string) {
		t.Helper()
		id, ok := readMenu()[label]
		if !ok {
			t.Fatalf("missing tray action %s", label)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := menu.CallWithContext(ctx, "com.canonical.dbusmenu.Event", 0, id, "clicked", dbus.MakeVariant(int32(0)), uint32(0)).Err; err != nil {
			t.Fatal(err)
		}
	}
	for _, label := range []string{"Open Vista", "Status", "Restart", "Stop", "Launch at login", "Quit desktop"} {
		if _, ok := readMenu()[label]; !ok {
			t.Fatalf("missing tray action %s", label)
		}
	}
	waitDesktop(t, "WebUI establishes an event stream", func() bool { s, e := readStatus(base); return e == nil && s.EventStreams >= 1 })
	initial, err := readStatus(base)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "web-first" && (initial.PID != firstService.PID || initial.InstanceID != firstService.InstanceID) {
		t.Fatal("the desktop did not reuse the web-started service")
	}
	if mode != "tray-at-launch" {
		if s := openWeb(); s.PID != initial.PID || s.InstanceID != initial.InstanceID {
			t.Fatal("web did not reuse the service open in the desktop")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		out, err := exec.CommandContext(ctx, binary, "status").Output()
		cancel()
		var status serviceStatus
		if err != nil || json.Unmarshal(out, &status) != nil || status.PID != initial.PID || status.InstanceID != initial.InstanceID {
			t.Fatalf("status did not report the shared service: %s: %v", out, err)
		}
	}
	click("Launch at login")
	waitDesktop(t, "login item enabled", func() bool { ok, _ := loginEnabled(binary); return ok })
	click("Launch at login")
	waitDesktop(t, "login item disabled", func() bool { ok, e := loginEnabled(binary); return e == nil && !ok })
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	out, err := exec.CommandContext(ctx, binary).CombinedOutput()
	cancel()
	if err != nil {
		t.Fatalf("repeat launch: %s: %v", out, err)
	}
	if got := itemList(); len(got) != 1 || got[0] != first[0] {
		t.Fatalf("unexpected tray registrations %v", got)
	}
	click("Open Vista")
	dir, err := instance.Dir()
	if err != nil {
		t.Fatal(err)
	}
	// The shared native control logic uses the same guarded lifecycle POST on
	// all platforms. Exercise it against the actual packaged service.
	if err := lifecycle(base, dir, initial.BinaryHash, "restart"); err != nil {
		t.Fatal(err)
	}
	var restarted serviceStatus
	waitDesktop(t, "WebUI reconnects after the native lifecycle restart", func() bool {
		s, e := readStatus(base)
		if e != nil || s.EventStreams != 1 || s.InstanceID == initial.InstanceID {
			return false
		}
		restarted = s
		return true
	})
	click("Quit desktop")
	select {
	case <-done:
		if exitErr != nil {
			t.Fatal(exitErr)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("desktop did not quit")
	}
	if s, e := readStatus(base); e != nil || s.InstanceID != restarted.InstanceID {
		t.Fatalf("quit changed local service: %+v: %v", s, e)
	}
	if err := lifecycle(base, dir, restarted.BinaryHash, "stop"); err != nil {
		t.Fatal(err)
	}
}
