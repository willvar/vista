package launch

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"vista/internal/instance"

	"github.com/spf13/cobra"
)

func TestLifecycleRequestsDoNotPreflightStatus(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"XDG_CONFIG_HOME", "APPDATA", "HOME"} {
		t.Setenv(name, root)
	}
	d, err := dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(d, 0700); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			calls := make(chan string, 4)
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls <- r.Method + " " + r.URL.Path
				if r.URL.Path == "/api/status" {
					// Real status sampling can exceed the former 300 ms
					// preflight deadline without the runtime being stopped.
					time.Sleep(400 * time.Millisecond)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer endpoint.Close()
			if err := instance.Write(d, instance.Info{PID: os.Getpid(), Address: endpoint.URL}); err != nil {
				t.Fatal(err)
			}
			path := "/api/status"
			if method == http.MethodPost {
				path = "/api/stop"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			response, err := requestContext(ctx, method, path)
			if err != nil {
				t.Fatalf("live runtime was rejected: %v", err)
			}
			response.Body.Close()
			if got := <-calls; got != method+" "+path {
				t.Fatalf("unrelated request: %s", got)
			}
			if len(calls) != 0 {
				t.Fatal("lifecycle request redundantly sampled status")
			}
		})
	}
}

func TestStartupCommandsRejectEphemeralAndInvalidPorts(t *testing.T) {
	for _, command := range []string{"start", "restart", "serve"} {
		for _, port := range []string{"0", "-1", "65536"} {
			t.Run(command+"/"+port, func(t *testing.T) {
				root := newRootCommand(func(bool) error { return nil })
				root.SetOut(io.Discard)
				root.SetErr(io.Discard)
				args := []string{"--port", port}
				if command != "" {
					args = append([]string{command}, args...)
				}
				root.SetArgs(args)
				if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "--port must be from 1 to 65535") {
					t.Fatalf("%v: expected invalid port before startup/stop, got %v", args, err)
				}
			})
		}
	}
}

func TestRestartValidatesConfiguredPortBeforeStopping(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("VISTA_PORT", "invalid")
	root := newRootCommand(func(bool) error { return nil })
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	for _, command := range root.Commands() {
		if command.Name() == "restart" {
			command.RunE = func(*cobra.Command, []string) error {
				t.Fatal("restart attempted to stop the server with invalid port configuration")
				return nil
			}
		}
	}
	root.SetArgs([]string{"restart"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "VISTA_PORT") {
		t.Fatalf("expected environment port validation, got %v", err)
	}
}

func TestDesktopLaunchIsNotRejectedByCobra(t *testing.T) {
	for _, inTray := range []bool{false, true} {
		called := false
		root := newRootCommand(func(tray bool) error {
			if tray != inTray {
				t.Fatalf("unexpected tray flag: %v", tray)
			}
			called = true
			return nil
		})
		if cobra.MousetrapHelpText != "" {
			t.Fatal("Cobra's Explorer guard would exit before Vista starts")
		}
		args := []string{}
		if inTray {
			args = append(args, "--tray")
		}
		root.SetArgs(args)
		if err := root.Execute(); err != nil || !called {
			t.Fatalf("desktop launcher did not run: called=%v error=%v", called, err)
		}
	}
}

func TestPublicHelpHasNoStartupConfigurationFlags(t *testing.T) {
	for _, command := range []string{"", "web", "serve", "restart"} {
		t.Run(command, func(t *testing.T) {
			var help bytes.Buffer
			root := newRootCommand(func(bool) error { t.Fatal("help launched the desktop"); return nil })
			root.SetOut(&help)
			args := []string{"--help"}
			if command != "" {
				args = append([]string{command}, args...)
			}
			root.SetArgs(args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			for _, flag := range []string{"--port", "--server", "--tray", "--daemon", "--no-browser"} {
				if strings.Contains(help.String(), flag) {
					t.Fatalf("unexpected public flag %s in %q", flag, help.String())
				}
			}
		})
	}
}
