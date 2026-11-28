package main

import (
	"context"
	"debug/buildinfo"
	"debug/elf"
	"errors"
	"fmt"
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
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

const windowRecoveryChild = "VISTA_TEST_WINDOW_RECOVERY_CHILD"

// Wails/GTK must run on the process's main OS thread, not a testing goroutine.
func TestMain(m *testing.M) {
	if os.Getenv(windowRecoveryChild) == "1" {
		if err := checkGTKWindowRecovery(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

func TestNativeLinuxWindowRecovery(t *testing.T) {
	if os.Getenv("VISTA_TEST_DESKTOP") == "" {
		t.Skip("requires the isolated native desktop check")
	}
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Fatal("requires a Wayland desktop")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	cmd.Env = append(os.Environ(), windowRecoveryChild+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("GTK window recovery: %v\n%s", err, out)
	}
}

// The packaged executable must dispatch its CLI even if WebKitGTK cannot load.
func TestNativeLinuxDesktopNoCGO(t *testing.T) {
	binary := os.Getenv("VISTA_TEST_DESKTOP")
	if binary == "" {
		t.Skip("requires the isolated native desktop check")
	}
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	cgo := ""
	for _, setting := range info.Settings {
		if setting.Key == "CGO_ENABLED" {
			cgo = setting.Value
			break
		}
	}
	if cgo != "0" {
		t.Fatalf("desktop CGO_ENABLED=%q, want 0", cgo)
	}
	image, err := elf.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	libs, err := image.ImportedLibraries()
	image.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, lib := range libs {
		if strings.Contains(lib, "gtk") || strings.Contains(lib, "webkit") {
			t.Fatalf("desktop directly links a graphical library: %s", lib)
		}
	}
	root := t.TempDir()
	// Shadow the soname for this process only so the check is meaningful
	// even on a desktop test host where the real library is installed.
	if err := os.WriteFile(filepath.Join(root, "libwebkit2gtk-4.1.so.0"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "status")
	cmd.Env = append(os.Environ(), "HOME="+root, "XDG_CONFIG_HOME="+root, "LD_LIBRARY_PATH="+root,
		"DISPLAY=", "WAYLAND_DISPLAY=", "WAYLAND_SOCKET=", "DBUS_SESSION_BUS_ADDRESS=")
	if out, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "Vista is stopped" {
		t.Fatalf("no-WebKit CLI: %v, %s", err, out)
	}
}

// Use an actual GTK window but inject availability so that testing the loss
// of a panel does not require stopping the user's desktop environment.
func checkGTKWindowRecovery() error {
	d := &desktop{flags: map[string]any{}}
	options := application.Options{Name: "Vista window recovery test"}
	if err := d.platformOptions(&options); err != nil {
		return err
	}
	d.app = application.New(options)
	loaded := make(chan string, 1)
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ready" {
			select {
			case loaded <- r.URL.RawQuery:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><script>fetch('/ready?bridge='+Boolean(window.webkit?.messageHandlers?.external)+'&dark='+matchMedia('(prefers-color-scheme:dark)').matches)</script>`)
	}))
	defer local.Close()
	intruded := make(chan struct{}, 1)
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case intruded <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer foreign.Close()
	d.base, _ = url.Parse(local.URL)
	d.home = d.open(local.URL)
	done := make(chan error, 1)
	d.app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(_ *application.ApplicationEvent) {
		go func() {
			err := verifyGTKNavigation(d, loaded, foreign.URL, intruded)
			if err == nil {
				err = exerciseGTKWindow(d)
			}
			done <- err
			d.app.Quit()
		}()
	})
	if err := d.app.Run(); err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	default:
		return errors.New("native window checks did not finish")
	}
}

func verifyGTKNavigation(d *desktop, loaded <-chan string, foreign string, intruded <-chan struct{}) error {
	select {
	case query := <-loaded:
		values, _ := url.ParseQuery(query)
		dark := false
		application.InvokeSync(func() { dark = d.app.Env.IsDarkMode() })
		if values.Get("bridge") != "false" || values.Get("dark") != strconv.FormatBool(dark) {
			return fmt.Errorf("unexpected WebView theme or native bridge state: %q, wanted dark=%t", query, dark)
		}
	case <-time.After(5 * time.Second):
		return errors.New("the protected Linux WebView did not load")
	}
	g, err := linuxGTK()
	if err != nil {
		return err
	}
	blocked := make(chan struct{}, 1)
	var ignore func(unsafe.Pointer)
	application.InvokeSync(func() {
		ignore = g.ignore
		// Observe the actual native WebKit decision before testing its effect.
		// The wrapper still calls the real C function on the GTK thread.
		g.ignore = func(decision unsafe.Pointer) {
			request := g.request(g.policyAction(decision))
			match := request != nil && g.uri(request) == foreign+"/"
			ignore(decision)
			if match {
				select {
				case blocked <- struct{}{}:
				default:
				}
			}
		}
		d.home.SetURL(foreign + "/")
	})
	defer application.InvokeSync(func() { g.ignore = ignore })
	select {
	case <-blocked:
		return nil
	case <-intruded:
		return errors.New("the Linux WebView reached a disallowed origin")
	case <-time.After(3 * time.Second):
		return errors.New("the navigation policy did not reject the foreign origin")
	}
}

func exerciseGTKWindow(d *desktop) error {
	visible := func(want bool) bool {
		for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(25 * time.Millisecond) {
			if d.home.IsVisible() == want {
				return true
			}
		}
		return false
	}
	application.InvokeSync(d.showHome)
	if !visible(true) {
		return errors.New("the desktop window did not show")
	}
	application.InvokeSync(func() { d.trayAvailability(true) })
	d.home.Close()
	if !visible(false) {
		return errors.New("closing with a tray did not hide the desktop window")
	}
	application.InvokeSync(d.showHome)
	if !visible(true) {
		return errors.New("tray activation did not restore the desktop window")
	}
	d.home.Close()
	if !visible(false) {
		return errors.New("the restored window was not hidden a second time")
	}
	application.InvokeSync(func() { d.trayAvailability(false) })
	if !visible(true) {
		return errors.New("losing the tray left the desktop window hidden")
	}
	return nil
}
