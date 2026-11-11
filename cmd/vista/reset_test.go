package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"vista/internal/appdirs"
	"vista/internal/instance"
)

// Exercise the built application's reset across a real port change, including
// a blocked target port and recovery using the still-reachable original URL.
func TestNativeResetPort(t *testing.T) {
	binary := os.Getenv("VISTA_TEST_BINARY")
	if binary == "" {
		t.Skip("set VISTA_TEST_BINARY to the application executable")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, name := range []string{"XDG_CONFIG_HOME", "APPDATA", "HOME"} {
		t.Setenv(name, root)
	}
	t.Setenv("VISTA_NO_BROWSER", "1")
	t.Setenv("VISTA_HOST", "0.0.0.0")
	config, err := appdirs.Config()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	reserve := func() net.Listener {
		t.Helper()
		// Reserve the application's full bind scope. A loopback-only listener
		// can coexist with a wildcard listener on macOS and Windows, so it
		// does not reliably make the application's target address unavailable.
		listener, err := net.Listen("tcp4", "0.0.0.0:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { listener.Close() })
		return listener
	}
	initial, target := reserve(), reserve()
	initialPort := initial.Addr().(*net.TCPAddr).Port
	targetPort := target.Addr().(*net.TCPAddr).Port
	initial.Close()
	// Initially the explicit launch port wins. After reset, external defaults
	// apply again, so the fresh runtime must move to targetPort.
	t.Setenv("VISTA_PORT", strconv.Itoa(targetPort))
	for name, contents := range map[string]string{
		"server.json":               `{"port":` + strconv.Itoa(initialPort) + `}`,
		"AGENTS.md":                 "old private instructions",
		"model-catalog-status.json": `{"nextCheckAt":"2099-01-01T00:00:00Z"}`,
	} {
		if err := os.WriteFile(filepath.Join(config, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cli := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		return string(output), err
	}
	t.Cleanup(func() {
		if output, err := cli("stop"); err != nil {
			t.Errorf("cleanup: %v %s", err, output)
			if runtime.GOOS != "windows" {
				// Preserve the real blocked shutdown before cleaning up this
				// isolated runtime; SIGQUIT emits the Go goroutine stacks.
				info, readErr := instance.Read(filepath.Join(config, "state"))
				if readErr == nil && instance.IsAlive(info.PID) {
					process, findErr := os.FindProcess(info.PID)
					if findErr == nil {
						_ = process.Signal(syscall.Signal(3))
						_ = process.Release()
						time.Sleep(200 * time.Millisecond)
					}
				}
			}
		}
		if t.Failed() {
			data, err := os.ReadFile(filepath.Join(config, "state", "server.log"))
			if len(data) > 80<<10 {
				data = append(data[:64<<10], data[len(data)-(16<<10):]...)
			}
			t.Logf("runtime log: %v\n%s", err, data)
		}
	})
	if output, err := cli("start", "--port", strconv.Itoa(initialPort)); err != nil {
		t.Fatalf("start: %v %s", err, output)
	}
	initialURL, targetURL := "http://127.0.0.1:"+strconv.Itoa(initialPort), "http://127.0.0.1:"+strconv.Itoa(targetPort)
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	defer client.CloseIdleConnections()
	reset := func() (int, []byte) {
		t.Helper()
		resp, err := client.Post(initialURL+"/api/reset", "application/json", strings.NewReader(`{"confirm":"reset-all"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, body
	}
	if status, body := reset(); status != http.StatusInternalServerError || !strings.Contains(string(body), "listen after reset") {
		t.Fatalf("occupied restart port was hidden: %d %s", status, body)
	}
	if _, err := os.Stat(filepath.Join(config, "server.json")); !os.IsNotExist(err) {
		t.Fatalf("saved port survived reset: %v", err)
	}
	target.Close()
	status, body := reset()
	var result struct {
		Reset   bool   `json:"reset"`
		Address string `json:"address"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &result) != nil || !result.Reset || result.Address != targetURL {
		t.Fatalf("restart response: %d %s", status, body)
	}
	resp, err := client.Get(targetURL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restarted runtime is unavailable: %d", resp.StatusCode)
	}
	if resp, err := client.Get(initialURL + "/api/status"); err == nil {
		resp.Body.Close()
		t.Fatal("original port is still listening after reset")
	}
	info, err := instance.Read(filepath.Join(config, "state"))
	if err != nil || info.Address != targetURL {
		t.Fatalf("instance discovery did not follow restart: %+v %v", info, err)
	}
	started := time.Now()
	if output, err := cli("status"); err != nil || !strings.Contains(output, targetURL) {
		t.Logf("CLI status after %s: %v %s", time.Since(started), err, output)
		current, readErr := instance.Read(filepath.Join(config, "state"))
		t.Logf("instance: %+v, read error: %v, process alive: %v", current, readErr, instance.IsAlive(current.PID))
		started = time.Now()
		response, requestErr := client.Get(targetURL + "/api/status")
		if requestErr != nil {
			t.Logf("direct status after %s: %v", time.Since(started), requestErr)
		} else {
			response.Body.Close()
			t.Logf("direct status after %s: HTTP %d", time.Since(started), response.StatusCode)
		}
		t.Fatal("CLI could not discover restarted runtime")
	}
}
