package main

import (
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

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
