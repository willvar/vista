package main

import (
	"context"
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
)

func TestMacLoginSetting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	executable := filepath.Join(home, "Vista & Test.app", "Contents", "MacOS", "vista-desktop")
	if ok, err := loginEnabled(executable); err != nil || ok {
		t.Fatalf("login=%v: %v", ok, err)
	}
	if err := setLogin(executable, true); err != nil {
		t.Fatal(err)
	}
	f, _ := macLoginFile()
	xml, err := os.ReadFile(f.path)
	if err != nil || !strings.Contains(string(xml), "Vista &amp; Test.app") {
		t.Fatalf("login plist %q: %v", xml, err)
	}
	if err := exec.Command("plutil", "-lint", f.path).Run(); err != nil {
		t.Fatalf("invalid LaunchAgent: %v", err)
	}
	if err := setLogin(executable, false); err != nil {
		t.Fatal(err)
	}
	if ok, err := loginEnabled(executable); err != nil || ok {
		t.Fatalf("login=%v: %v", ok, err)
	}
}

// Run from an Aqua user session. This is a bundle/real WKWebView smoke test,
// not a substitute for exercising the macOS menu bar and login with AppKit.
func TestNativeDarwinDesktop(t *testing.T) {
	binary := os.Getenv("VISTA_TEST_DESKTOP")
	if binary == "" {
		t.Skip("VISTA_TEST_DESKTOP must point into the unpacked Vista.app")
	}
	if !strings.HasSuffix(binary, ".app/Contents/MacOS/vista-desktop") {
		t.Fatal("expected the unpacked app bundle")
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("VISTA_HOST", "127.0.0.1")
	t.Setenv("VISTA_NO_BROWSER", "1")
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	t.Setenv("VISTA_PORT", strconv.Itoa(port))
	base, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d/", port))
	log, err := os.Create(filepath.Join(root, "desktop.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	desktop := exec.Command(binary)
	desktop.Stdout, desktop.Stderr = log, log
	if err := desktop.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = desktop.Process.Kill()
		_ = desktop.Wait()
		ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, filepath.Join(filepath.Dir(binary), "vista"), "stop").Run()
		if t.Failed() {
			body, _ := os.ReadFile(log.Name())
			t.Logf("desktop output: %s", body)
		}
	}()
	var initial serviceStatus
	for deadline := time.Now().Add(25 * time.Second); time.Now().Before(deadline); time.Sleep(150 * time.Millisecond) {
		if s, err := readStatus(base); err == nil && s.EventStreams > 0 {
			initial = s
			break
		}
	}
	if initial.PID == 0 {
		t.Fatal("the packaged app's WKWebView did not connect")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, binary).CombinedOutput(); err != nil {
		t.Fatalf("desktop did not reuse first instance: %s: %v", out, err)
	}
	if now, err := readStatus(base); err != nil || now.InstanceID != initial.InstanceID || now.EventStreams != 1 {
		t.Fatalf("unexpected service after repeat launch: %+v: %v", now, err)
	}
}
