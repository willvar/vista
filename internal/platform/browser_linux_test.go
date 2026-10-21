//go:build linux

package platform

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestOpenBrowserPassesURLWithoutWaitingForLauncherExit(t *testing.T) {
	d := t.TempDir()
	output := filepath.Join(d, "opened-url")
	launcher := filepath.Join(d, "xdg-open")
	gate := filepath.Join(d, "launcher-gate")
	if err := syscall.Mkfifo(gate, 0600); err != nil {
		t.Fatal(err)
	}
	pipe, err := os.OpenFile(gate, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pipe.WriteString("done\n"); _ = pipe.Close() }()
	script := "#!/bin/sh\nprintf '%s' \"$1\" > \"$VISTA_BROWSER_TEST_OUTPUT\"\nread done < \"$VISTA_BROWSER_TEST_GATE\"\n"
	if err := os.WriteFile(launcher, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", d)
	t.Setenv("VISTA_BROWSER_TEST_OUTPUT", output)
	t.Setenv("VISTA_BROWSER_TEST_GATE", gate)
	url := "http://127.0.0.1:5186/"
	returned := make(chan error, 1)
	go func() { returned <- OpenBrowser(url) }()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("opening the browser waited for the launcher to exit")
	}
	var got []byte
	deadline := time.Now().Add(time.Second)
	for {
		got, err = os.ReadFile(output)
		if err == nil && string(got) == url {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("launcher did not receive URL: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	if strings.TrimSpace(string(got)) != url {
		t.Fatalf("launcher received %q, want %q", got, url)
	}
	if err := os.Remove(launcher); err != nil {
		t.Fatal(err)
	}
	if err := OpenBrowser(url); err == nil {
		t.Fatal("missing desktop launcher must report its startup error")
	}
}
