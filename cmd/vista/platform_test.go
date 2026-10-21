package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"vista/internal/appdirs"
	"vista/internal/instance"
	"vista/internal/platform"
	"vista/internal/store"
)

// Run against the built application, including its real subprocess Workers.
// An explicit binary keeps ordinary unit tests free of builds and live state.
func TestNativeApplication(t *testing.T) {
	binary := os.Getenv("VISTA_TEST_BINARY")
	if binary == "" {
		t.Skip("set VISTA_TEST_BINARY to the application executable")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	t.Setenv("HOME", root)
	t.Setenv("VISTA_NO_BROWSER", "1")
	config, err := appdirs.Config()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	status := fmt.Sprintf(`{"nextCheckAt":%q}`, time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(config, "model-catalog-status.json"), []byte(status), 0600); err != nil {
		t.Fatal(err)
	}
	var shellEvidence, runtimeEvidence atomic.Bool
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			io.WriteString(w, `{"data":[{"id":"platform-fixture"}]}`)
			return
		}
		var request struct {
			Messages []struct{ Role, Content string }
			Tools    []json.RawMessage
		}
		if r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Messages) == 0 {
			http.Error(w, "unexpected model request", 400)
			return
		}
		delta := map[string]any{"content": "Platform verified."}
		finish := "stop"
		last := request.Messages[len(request.Messages)-1]
		if len(request.Tools) > 0 {
			for _, message := range request.Messages {
				if message.Role == "system" && strings.Contains(message.Content, "Runtime OS: "+runtime.GOOS+"; shell: "+platform.ShellName()) {
					runtimeEvidence.Store(true)
				}
				if message.Role == "tool" && strings.Contains(message.Content, "Vista 中文") && strings.Contains(message.Content, `"exitCode":7`) {
					shellEvidence.Store(true)
				}
			}
			if last.Role == "user" {
				name, script := "shell", "printf 'Vista 中文'; exit 7"
				if runtime.GOOS == "windows" {
					script = "Write-Output 'Vista 中文'; exit 7"
				}
				if strings.HasPrefix(last.Content, "background-") || last.Content == "cancel-check" {
					script = applicationSleepCommand(filepath.Join(root, last.Content+".pid"))
					if strings.HasPrefix(last.Content, "background-") {
						name = "start_background"
					}
				}
				args, _ := json.Marshal(map[string]any{"directory": root, "command": script, "timeoutSeconds": 60})
				delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_" + last.Content, "type": "function", "function": map[string]any{"name": name, "arguments": string(args)}}}}
				finish = "tool_calls"
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}, "usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer model.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	t.Setenv("VISTA_PORT", strconv.Itoa(port))
	cli := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		return string(output), err
	}
	t.Cleanup(func() {
		if output, err := cli("stop"); err != nil {
			t.Errorf("cleanup stop: %v %s", err, output)
		}
		if t.Failed() {
			for _, name := range []string{"server.log", "worker.log"} {
				data, _ := os.ReadFile(filepath.Join(config, "state", name))
				t.Logf("%s:\n%s", name, data)
			}
		}
	})
	if output, err := cli("start"); err != nil {
		t.Fatalf("start: %v %s", err, output)
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	api := func(method, path string, input, output any) {
		t.Helper()
		var body io.Reader
		if input != nil {
			encoded, _ := json.Marshal(input)
			body = bytes.NewReader(encoded)
		}
		req, _ := http.NewRequest(method, endpoint+"/api"+path, body)
		req.Header.Set("Content-Type", "application/json")
		client := http.Client{Timeout: 15 * time.Second}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			t.Fatalf("%s %s: HTTP %d %s", method, path, response.StatusCode, data)
		}
		if output != nil && json.Unmarshal(data, output) != nil {
			t.Fatalf("invalid JSON from %s: %s", path, data)
		}
	}
	var instanceBefore instance.Info
	instanceBefore, err = instance.Read(filepath.Join(config, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if output, err := cli("start"); err != nil {
		t.Fatalf("duplicate start: %v %s", err, output)
	}
	if after, _ := instance.Read(filepath.Join(config, "state")); after.PID != instanceBefore.PID {
		t.Fatal("duplicate start launched another Server")
	}
	if output, err := cli("serve"); err == nil || !strings.Contains(output, "already running") {
		t.Fatalf("instance lock did not reject duplicate server: %v %s", err, output)
	}
	for _, path := range []string{"/", "/app.js", "/vista-web.css"} {
		response, err := http.Get(endpoint + path)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 || len(data) == 0 {
			t.Fatalf("WebUI asset %s is unavailable", path)
		}
	}
	// Persist and replace configuration through the same APIs as the WebUI.
	api("POST", "/provider-config", map[string]any{"name": "Platform fixture", "providerId": "custom", "provider": "openai-chat", "credentialMode": "none", "baseUrl": model.URL + "/v1"}, nil)
	for _, text := range []string{"First preference.", "Verified preference."} {
		api("PUT", "/agent-settings", map[string]any{"instructions": text, "delegationDepth": 0}, nil)
	}
	var session store.Session
	api("POST", "/sessions", map[string]string{"title": "platform check"}, &session)
	sessionPath := "/sessions/" + session.ID
	waitRun := func(runID, status string) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		var after int64
		for time.Now().Before(deadline) {
			var events []store.Event
			api("GET", sessionPath+"/event-page?after="+strconv.FormatInt(after, 10), nil, &events)
			for _, event := range events {
				after = event.Sequence
				var data struct{ RunID, Status, Content string }
				_ = json.Unmarshal(event.Data, &data)
				if event.Kind == "run_finished" && data.RunID == runID {
					if data.Status != status {
						t.Fatalf("run status=%s, want %s: %s", data.Status, status, event.Data)
					}
					return
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatal("run did not finish")
	}
	submit := func(prompt string) string {
		var run struct{ RunID string }
		api("POST", sessionPath+"/messages", map[string]string{"content": prompt}, &run)
		return run.RunID
	}
	waitRun(submit("shell-check"), "succeeded")
	if !shellEvidence.Load() || !runtimeEvidence.Load() {
		t.Fatalf("model missed shell evidence=%v or runtime context=%v", shellEvidence.Load(), runtimeEvidence.Load())
	}
	waitPID := func(prompt string) int {
		t.Helper()
		path := filepath.Join(root, prompt+".pid")
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			data, err := os.ReadFile(path)
			pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
			if err == nil && pid > 0 {
				return pid
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatal("shell child did not start")
		return 0
	}
	waitGone := func(pid int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if !platform.IsAlive(pid) {
				return
			}
			if runtime.GOOS == "linux" {
				data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
				if err != nil || strings.Contains(string(data), ") Z ") {
					return
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("child %d survived stop", pid)
	}
	runID := submit("cancel-check")
	child := waitPID("cancel-check")
	api("POST", sessionPath+"/cancel", nil, nil)
	waitRun(runID, "interrupted")
	waitGone(child)
	for _, action := range []string{"stop", "close", "restart", "crash"} {
		t.Logf("background lifecycle: %s", action)
		prompt := "background-" + action
		waitRun(submit(prompt), "succeeded")
		child := waitPID(prompt)
		var jobs []store.Job
		api("GET", sessionPath+"/jobs", nil, &jobs)
		var active store.Job
		for _, job := range jobs {
			if job.Status == "running" {
				active = job
			}
		}
		if active.ID == "" {
			t.Fatal("background job was lost at the reply boundary")
		}
		api("GET", sessionPath+"/jobs/"+active.ID+"/output", nil, new(map[string]any))
		switch action {
		case "stop":
			api("POST", sessionPath+"/jobs/"+active.ID+"/stop", nil, nil)
		case "close":
			api("POST", sessionPath+"/close", nil, nil)
			api("POST", sessionPath+"/open", nil, nil)
		case "restart":
			if output, err := cli("restart"); err != nil {
				t.Fatalf("restart: %v %s", err, output)
			}
		case "crash":
			info, err := instance.Read(filepath.Join(config, "state"))
			if err != nil {
				t.Fatal(err)
			}
			server, err := os.FindProcess(info.PID)
			if err != nil {
				t.Fatal(err)
			}
			if err := server.Kill(); err != nil {
				t.Fatal(err)
			}
			server.Release()
			if output, err := cli("start"); err != nil {
				t.Fatalf("crash recovery: %v %s", err, output)
			}
		}
		waitGone(child)
	}
	var settings struct{ Instructions string }
	api("GET", "/agent-settings", nil, &settings)
	if settings.Instructions != "Verified preference." {
		t.Fatal("settings did not survive restart")
	}
	if output, err := cli("status"); err != nil || !strings.Contains(output, `"state": "running"`) {
		t.Fatalf("status: %v %s", err, output)
	}
	if output, err := cli("stop"); err != nil {
		t.Fatalf("stop: %v %s", err, output)
	}
	if output, err := cli("status"); err != nil || !strings.Contains(output, "stopped") {
		t.Fatalf("status after stop: %v %s", err, output)
	}
}

func applicationSleepCommand(path string) string {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	prefix := ""
	if runtime.GOOS == "windows" {
		quote = func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
		prefix = "& "
	}
	return prefix + quote(os.Args[0]) + " " + quote("-test.run=^TestApplicationSleepHelper$") + " '--' '--vista-sleep-helper' " + quote(path)
}

func TestApplicationSleepHelper(t *testing.T) {
	args := os.Args
	if len(args) < 3 || args[len(args)-2] != "--vista-sleep-helper" {
		return
	}
	if err := os.WriteFile(args[len(args)-1], []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		os.Exit(2)
	}
	for {
		time.Sleep(time.Hour)
	}
}
