package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"vista/internal/instance"
	"vista/internal/platform"
)

func main() {
	server := flag.String("server", "", "Connect to an existing Vista at a numeric loopback HTTP origin")
	backend := flag.String("backend", "", "Path to the companion Vista executable")
	tray := flag.Bool("tray", false, "Open in the system tray if one is available")
	flag.Parse()
	if flag.NArg() != 0 || (*server != "" && *backend != "") {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*server, *backend, *tray); err != nil {
		fmt.Fprintln(os.Stderr, "Vista Desktop:", err)
		startupError(err)
		os.Exit(1)
	}
}

// Resolve the supplied development companion or the one delivered beside the desktop.
func companionPath(backend string) (string, error) {
	if backend == "" {
		self, err := os.Executable()
		if err != nil {
			return "", err
		}
		backend = filepath.Join(filepath.Dir(self), "vista"+executableSuffix)
	}
	return filepath.Abs(backend)
}

func connect(server, backend string) (*url.URL, string, serviceStatus, error) {
	explicit := server != ""
	if !explicit {
		path, err := companionPath(backend)
		if err != nil {
			return nil, "", serviceStatus{}, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, path, "start")
		platform.PrepareCommand(cmd)
		cmd.Env = append(os.Environ(), "VISTA_HOST=127.0.0.1", "VISTA_NO_BROWSER=1")
		out, err := cmd.Output()
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return nil, "", serviceStatus{}, fmt.Errorf("start Vista: %s", strings.TrimSpace(string(exit.Stderr)))
			}
			return nil, "", serviceStatus{}, fmt.Errorf("start Vista: %w", err)
		}
		server = strings.TrimSpace(string(out))
	}
	if !instance.IsLocalAddress(server) {
		return nil, "", serviceStatus{}, errors.New("expected a Vista HTTP origin with a numeric local address and port")
	}
	base, err := url.Parse(server)
	if err != nil {
		return nil, "", serviceStatus{}, err
	}
	if explicit && !net.ParseIP(base.Hostname()).IsLoopback() {
		return nil, "", serviceStatus{}, errors.New("--server only accepts a loopback address")
	}
	base.Path = "/"
	status, err := readStatus(base)
	if err != nil {
		return nil, "", serviceStatus{}, err
	}
	dir, _ := instance.Dir()
	if !registered(dir, base, status.PID) {
		dir = "" // Explicitly attached instances need not use this configuration.
		if !explicit {
			return nil, "", serviceStatus{}, errors.New("Vista startup did not match the registered instance")
		}
	}
	return base, dir, status, nil
}

var serviceHTTP = http.Client{
	Transport:     &http.Transport{}, // Never use a proxy for the local service.
	Timeout:       4 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

type serviceStatus struct {
	EventStreams int                                  `json:"eventStreams"`
	BinaryHash   string                               `json:"binaryHash"`
	PID          int                                  `json:"pid"`
	Version      string                               `json:"version"`
	InstanceID   string                               `json:"instanceId"`
	HostOS       string                               `json:"hostOS"`
	DefaultModel string                               `json:"defaultModel"`
	Uptime       int                                  `json:"uptimeSeconds"`
	Sessions     struct{ Open, Running, Workers int } `json:"sessions"`
	Jobs         int                                  `json:"managedBackgroundProcesses"`
	Memory       struct {
		RSS uint64 `json:"rssBytes"`
	} `json:"memory"`
}

func readStatus(base *url.URL) (serviceStatus, error) {
	var status serviceStatus
	resp, err := serviceHTTP.Get(base.JoinPath("api/status").String())
	if err != nil {
		return status, fmt.Errorf("check Vista connection: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 512<<10)).Decode(&status) != nil || status.PID < 1 || status.Version == "" || status.InstanceID == "" {
		return status, errors.New("the selected address did not return a Vista instance")
	}
	return status, nil
}

func registered(dir string, base *url.URL, pid int) bool {
	if dir == "" {
		return false
	}
	entry, err := instance.Read(dir)
	return err == nil && entry.PID == pid && instance.IsAlive(pid) && strings.TrimRight(entry.Address, "/") == strings.TrimRight(base.String(), "/")
}

type desktop struct {
	app             *application.App
	base            *url.URL // Read and updated on the native UI thread.
	dir             string
	err             error
	home            *application.WebviewWindow
	flags           map[string]any
	tray            trayState
	startInTray     bool
	loginExecutable string
	desktopState
}

func windowOptions() application.WebviewWindowOptions {
	return application.WebviewWindowOptions{
		Title: "Vista", Width: 1160, Height: 790,
		Permissions: map[application.PermissionType]application.Permission{
			application.PermissionCamera:     application.PermissionDeny,
			application.PermissionMicrophone: application.PermissionDeny,
		},
	}
}

func (d *desktop) internal(target *url.URL) bool {
	if target.Scheme != "http" {
		return false
	}
	if target.Host == d.base.Host {
		return true
	}
	// The shared WebUI can move to a new port after a reset or port change.
	// Trust only the exact new root recorded by the same local configuration.
	if d.dir == "" || target.Hostname() != d.base.Hostname() || !instance.IsLocalAddress(target.String()) {
		return false
	}
	entry, err := instance.Read(d.dir)
	if err != nil || !instance.IsAlive(entry.PID) || strings.TrimRight(entry.Address, "/") != strings.TrimRight(target.String(), "/") {
		return false
	}
	d.base = target
	d.originChanged()
	return true
}

func (d *desktop) navigate(raw string, popup, clicked bool) bool {
	if raw == "about:blank" {
		return !popup
	}
	target, err := url.Parse(raw)
	if err != nil || target.User != nil || target.Hostname() == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return false
	}
	if d.internal(target) {
		if popup && clicked {
			go application.InvokeAsync(func() { d.open(target.String()) })
		}
		return !popup
	}
	if clicked {
		if err := platform.OpenBrowser(target.String()); err != nil {
			d.app.Logger.Error("Could not open the system browser", "error", err)
		}
	}
	return false
}

func desktopInstanceID(root, target string) string {
	prefix := desktopID + "."
	if target == "linux" {
		// D-Bus forbids an element that begins with a digit. Keep the old
		// Windows name so existing Windows desktop processes are still detected.
		prefix += "config_"
	}
	return fmt.Sprintf("%s%x", prefix, sha256.Sum256([]byte(root)))
}

func run(server, backend string, startInTray bool) error {
	d := &desktop{flags: map[string]any{}, startInTray: startInTray}
	root, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	if !filepath.IsAbs(root) {
		return errors.New("the user configuration directory must be absolute")
	}
	if server == "" && backend == "" {
		d.loginExecutable, err = os.Executable()
		if err != nil {
			return err
		}
	}
	options := application.Options{Name: "Vista", Flags: d.flags, SingleInstance: &application.SingleInstanceOptions{
		UniqueID: desktopInstanceID(root, runtime.GOOS),
		OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
			if !slices.Contains(data.Args, "--tray") && !slices.Contains(data.Args, "-tray") {
				application.InvokeAsync(d.showHome)
			}
		},
	}}
	if err := d.platformOptions(&options); err != nil {
		return err
	}
	d.app = application.New(options)
	base, dir, status, err := connect(server, backend)
	if err != nil {
		return err
	}
	d.base, d.dir = base, dir
	platformInfo := d.app.Env.Info().PlatformInfo
	for _, key := range []string{"webkit2gtk-runtime", "WebView2"} {
		if version, ok := platformInfo[key].(string); ok && version != "" {
			d.flags["vistaWebViewVersion"] = version
			break
		}
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "github.com/wailsapp/wails/v3" {
				d.flags["vistaWailsVersion"] = strings.TrimPrefix(dep.Version, "v")
				break
			}
		}
	}
	d.setup(backend, status)
	if !d.tray.created {
		d.startInTray = false
	}
	d.home = d.open(base.String())
	return errors.Join(d.app.Run(), d.err)
}
