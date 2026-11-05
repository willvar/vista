package main

import (
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
	for _, command := range []string{"", "start", "restart", "serve"} {
		for _, port := range []string{"0", "-1", "65536"} {
			t.Run(command+"/"+port, func(t *testing.T) {
				root := newRootCommand()
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
	root := newRootCommand()
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
	root := newRootCommand()
	if cobra.MousetrapHelpText != "" {
		t.Fatal("Cobra's Explorer guard would exit before Vista starts")
	}
	called := false
	root.PreRunE = nil
	root.RunE = func(*cobra.Command, []string) error { called = true; return nil }
	root.SetArgs([]string{})
	if err := root.Execute(); err != nil || !called {
		t.Fatalf("no-argument launcher did not run: called=%v error=%v", called, err)
	}
}
