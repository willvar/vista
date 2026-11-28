package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
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
	if !strings.HasSuffix(binary, ".app/Contents/MacOS/vista") {
		t.Fatal("expected the unpacked app bundle")
	}
	for _, webFirst := range []bool{false, true} {
		name := "desktop-first"
		if webFirst {
			name = "web-first"
		}
		t.Run(name, func(t *testing.T) { testMacDesktop(t, binary, webFirst) })
	}
}

func testMacDesktop(t *testing.T, binary string, webFirst bool) {
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
	config := filepath.Join(root, "Library", "Application Support", "vista")
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "model-catalog-status.json"), []byte(`{"nextCheckAt":"2099-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cli := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s: %v", args, out, err)
		}
		return out
	}
	if got := strings.TrimSpace(string(cli("status"))); got != "Vista is stopped" {
		t.Fatalf("stopped status = %q", got)
	}
	t.Cleanup(func() { cli("stop") })
	var first serviceStatus
	if webFirst {
		if got := strings.TrimSpace(string(cli("web"))); got != base.String() {
			t.Fatalf("web URL = %q", got)
		}
		first, err = readStatus(base)
		if err != nil || first.EventStreams != 0 {
			t.Fatalf("web started a desktop: %+v: %v", first, err)
		}
	}
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
	if webFirst && (initial.PID != first.PID || initial.InstanceID != first.InstanceID) {
		t.Fatal("the desktop did not reuse the web-started service")
	}
	if got := strings.TrimSpace(string(cli("web"))); got != base.String() {
		t.Fatalf("web URL = %q", got)
	}
	var reported serviceStatus
	if err := json.Unmarshal(cli("status"), &reported); err != nil || reported.PID != initial.PID || reported.InstanceID != initial.InstanceID {
		t.Fatalf("web and status did not preserve the desktop service: %+v: %v", reported, err)
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

const macNavigationProbe = "VISTA_TEST_WK_NAVIGATION_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(macNavigationProbe) == "1" {
		if err := checkMacNavigation(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

func TestNativeMacNavigation(t *testing.T) {
	if os.Getenv("VISTA_TEST_DESKTOP") == "" {
		t.Skip("requires an Aqua session")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	cmd.Env = append(os.Environ(), macNavigationProbe+"=1", "HOME="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native navigation: %v: %s", err, out)
	}
}

func checkMacNavigation() error {
	foreign := make(chan struct{}, 1)
	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case foreign <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer outside.Close()
	stayed := make(chan string, 1)
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/proof" {
			select {
			case stayed <- r.URL.Query().Get("bridge"):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!doctype html><script>
			const bridge = Boolean(window.webkit?.messageHandlers?.external);
			setTimeout(() => fetch('/proof?bridge=' + bridge), 600);
			location.assign(%q);
		</script>`, outside.URL)
	}))
	defer local.Close()
	base, _ := url.Parse(local.URL)
	d := &desktop{base: base, flags: map[string]any{}}
	d.app = application.New(application.Options{Name: "Vista navigation test"})
	d.home = d.open(base.String())
	done := make(chan error, 1)
	d.app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(_ *application.ApplicationEvent) {
		go func() {
			select {
			case <-foreign:
				done <- errors.New("WKWebView reached a disallowed origin")
			case bridge := <-stayed:
				if bridge != "false" {
					done <- errors.New("WKWebView exposed the native bridge")
				} else {
					done <- nil
				}
			case <-time.After(9 * time.Second):
				done <- errors.New("protected WKWebView did not load its allowed origin")
			}
			d.app.Quit()
		}()
	})
	if err := d.app.Run(); err != nil {
		return err
	}
	if d.err != nil {
		return d.err
	}
	select {
	case err := <-done:
		return err
	default:
		return errors.New("WKWebView closed before the navigation check")
	}
}
