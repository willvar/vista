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
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vista/internal/appdirs"
	"vista/internal/instance"
	"vista/internal/platform"
	"vista/internal/store"
	"vista/internal/workerproto"
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
	var shellEvidence, runtimeEvidence, delegationEvidence, childToolEvidence, reportEvidence atomic.Bool
	completionStarted, resumeCompletion := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(resumeCompletion) })
	var holdCompletion sync.Once
	restartWaiting := make(chan string, 4)
	var heldRestarts sync.Map
	var restartEvidence atomic.Int32
	const discoveredModel = "platform-discovered"
	var delayInventory atomic.Bool
	inventoryRequested, inventoryReady := make(chan struct{}, 1), make(chan struct{})
	releaseInventory := sync.OnceFunc(func() { close(inventoryReady) })
	longTask := strings.Repeat("界", workerproto.MaxPromptBytes/3)
	longTask += strings.Repeat("x", workerproto.MaxPromptBytes-len(longTask))
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			if delayInventory.Load() {
				select {
				case inventoryRequested <- struct{}{}:
				default:
				}
				select {
				case <-inventoryReady:
				case <-r.Context().Done():
					return
				}
			}
			fmt.Fprintf(w, `{"data":[{"id":"platform-fixture","input_modalities":["text"],"supports_tools":true},{"id":%q,"input_modalities":["text"],"supports_tools":true}]}`, discoveredModel)
			return
		}
		var request struct {
			Model    string
			Messages []struct{ Role, Content string }
			Tools    []json.RawMessage
		}
		if r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Messages) == 0 {
			http.Error(w, "unexpected model request", 400)
			return
		}
		// Completion notices may follow a user input at the same decision
		// boundary. The fixture must handle that input rather than treating
		// the trailing runtime evidence as the entire request.
		isRuntimeNotice := func(role, content string) bool {
			return role == "user" && (strings.HasPrefix(content, "Runtime completion event.") || strings.HasPrefix(content, "Vista service restart."))
		}
		last := request.Messages[len(request.Messages)-1]
		if isRuntimeNotice(last.Role, last.Content) && strings.Contains(last.Content, `"kind":"job"`) {
			// Hold the automatic continuation so the next lifecycle prompt
			// deterministically exercises input delivery during an active run.
			holdCompletion.Do(func() {
				close(completionStarted)
				select {
				case <-resumeCompletion:
				case <-r.Context().Done():
				}
			})
		}
		for i := len(request.Messages) - 1; i >= 0; i-- {
			if request.Messages[i].Role != "system" && !isRuntimeNotice(request.Messages[i].Role, request.Messages[i].Content) {
				last = request.Messages[i]
				break
			}
		}
		original, restarted, completed := "", false, false
		for _, message := range request.Messages {
			if message.Role == "user" && !isRuntimeNotice(message.Role, message.Content) {
				original, restarted, completed = message.Content, false, false
			}
			if strings.HasPrefix(message.Content, "Vista service restart.") {
				restarted = true
			}
			if message.Role == "tool" && strings.Contains(message.Content, "Vista 中文") {
				completed = true
			}
		}
		if strings.HasPrefix(original, "resume-") && last.Role == "tool" && !restarted {
			if _, held := heldRestarts.LoadOrStore(original, true); !held {
				restartWaiting <- original
			}
			<-r.Context().Done()
			return
		}
		if last.Role == "user" && strings.HasPrefix(last.Content, "provider-error-") {
			message := strings.Repeat("upstream failure ", 1000)
			if last.Content == "provider-error-unicode" {
				message = strings.Repeat("上游返回较长的错误详情。", 1000)
			}
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": message}})
			return
		}
		delta := map[string]any{"content": "Platform verified."}
		finish := "stop"
		toolCall := func(name string, input any) {
			args, _ := json.Marshal(input)
			id := "call_" + last.Content
			if len(id) > 256 {
				id = "call_fixture"
			}
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(args)}}}}
			finish = "tool_calls"
		}
		if strings.HasPrefix(original, "resume-") && restarted {
			if !completed {
				t.Error("restart lost a completed tool result")
			}
			if original == "resume-restart" && request.Model != discoveredModel {
				t.Errorf("restart replaced the manually selected model: %q", request.Model)
			}
			restartEvidence.Add(1)
			delta["content"] = "Restart continuation verified."
		} else if last.Role == "user" && last.Content == "long-finish-reason" {
			finish = strings.Repeat("upstream-reason-", 100)
		} else if len(request.Tools) > 0 {
			for _, message := range request.Messages {
				if message.Role == "system" && strings.HasPrefix(message.Content, "Runtime completion event.") {
					t.Error("runtime completion acquired system authority")
				}
				if isRuntimeNotice(message.Role, message.Content) && strings.Contains(message.Content, `"kind":"agent"`) {
					reportEvidence.Store(true)
				}
				if message.Role == "system" && strings.Contains(message.Content, "Runtime OS: "+runtime.GOOS+"; shell: "+platform.ShellName()) {
					runtimeEvidence.Store(true)
				}
				if message.Role == "tool" && strings.Contains(message.Content, "Vista 中文") && strings.Contains(message.Content, `"exitCode":7`) {
					shellEvidence.Store(true)
				}
				if message.Role == "tool" && strings.Contains(message.Content, "unsupported tool") {
					childToolEvidence.Store(true)
				}
			}
			if last.Role == "user" && last.Content == longTask {
				delegationEvidence.Store(true)
				// An unsupported call must return a tool error, including when
				// the child prefix extends a maximum-length provider tool name.
				toolCall(strings.Repeat("a", 128), map[string]any{})
			} else if last.Role == "user" {
				name, script := "shell", "printf 'Vista 中文'; exit 7"
				if runtime.GOOS == "windows" {
					script = "Write-Output 'Vista 中文'; exit 7"
				}
				if strings.HasPrefix(last.Content, "resume-") {
					path := filepath.Join(root, last.Content+".effects")
					if runtime.GOOS == "windows" {
						script = "Add-Content -LiteralPath '" + strings.ReplaceAll(path, "'", "''") + "' -Value 'completed'; " + script
					} else {
						script = "printf 'completed\\n' >> '" + strings.ReplaceAll(path, "'", "'\\''") + "'; " + script
					}
				}
				if strings.HasPrefix(last.Content, "background-") || last.Content == "cancel-check" {
					script = applicationSleepCommand(filepath.Join(root, last.Content+".pid"))
					if strings.HasPrefix(last.Content, "background-") {
						name = "start_background"
						script = strings.Repeat("# 中文🙂\n", 600) + script
					}
				}
				input := map[string]any{"directory": root, "command": script}
				if name == "shell" {
					input["timeoutSeconds"] = 60
				}
				if last.Content == "delegation-check" {
					name, input = "delegate", map[string]any{"task": longTask}
				}
				toolCall(name, input)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}, "usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer func() { releaseInventory(); resume(); model.Close() }()
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
	indexResponse, err := http.Get(endpoint + "/")
	if err != nil {
		t.Fatal(err)
	}
	indexData, err := io.ReadAll(indexResponse.Body)
	indexResponse.Body.Close()
	if err != nil || indexResponse.StatusCode != http.StatusOK {
		t.Fatal("WebUI index is unavailable", err)
	}
	paths := []string{"/"}
	for _, match := range regexp.MustCompile(`(?:src|href)="(/assets/[^"\s]+)"`).FindAllSubmatch(indexData, -1) {
		paths = append(paths, string(match[1]))
	}
	if len(paths) < 3 {
		t.Fatal("WebUI index does not reference JavaScript and styles")
	}
	for _, path := range paths {
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
	api("POST", "/provider-config", map[string]any{"name": "Platform fixture", "providerId": "custom", "provider": "openai-chat", "credentialMode": "none", "baseUrl": model.URL + "/v1", "ids": []string{"platform-fixture"}}, nil)
	var instructionVersion struct{ InstructionsSHA256 string }
	api("GET", "/agent-settings", nil, &instructionVersion)
	for _, text := range []string{"First preference.", "Verified preference."} {
		api("PUT", "/agent-settings", map[string]any{"instructions": text, "expectedSha256": instructionVersion.InstructionsSHA256, "delegationDepth": 0}, &instructionVersion)
		if len(instructionVersion.InstructionsSHA256) != 64 {
			t.Fatal("instruction write did not return its committed version")
		}
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
		t.Fatalf("run %s did not finish after event %d", runID, after)
	}
	submit := func(prompt string) string {
		t.Helper()
		var run struct {
			RunID string
			Input *store.QueuedInput
			Event store.Event
		}
		api("POST", sessionPath+"/messages", map[string]string{"content": prompt}, &run)
		if prompt == "background-close" {
			resume()
			if run.Input == nil {
				t.Fatal("background continuation did not keep the session available for input")
			}
		}
		if run.RunID != "" {
			return run.RunID
		}
		if run.Input == nil {
			t.Fatalf("submission %q returned neither a run nor an input", prompt)
		}
		after := run.Event.Sequence
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			var events []store.Event
			api("GET", sessionPath+"/event-page?after="+strconv.FormatInt(after, 10), nil, &events)
			for _, event := range events {
				after = event.Sequence
				var delivered struct{ ID, RunID string }
				if event.Kind == "input_delivered" && json.Unmarshal(event.Data, &delivered) == nil && delivered.ID == run.Input.ID && delivered.RunID != "" {
					return delivered.RunID
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("input %s for %q was not delivered", run.Input.ID, prompt)
		return ""
	}
	waitRun(submit("shell-check"), "succeeded")
	if !shellEvidence.Load() || !runtimeEvidence.Load() {
		t.Fatalf("model missed shell evidence=%v or runtime context=%v", shellEvidence.Load(), runtimeEvidence.Load())
	}
	// Cover the complete delegate registration/ACK, child request, terminal
	// progress and report collection path with the largest permitted prompt.
	api("PUT", "/agent-settings", map[string]any{"delegationDepth": 1}, nil)
	workerPID := func() int {
		var status struct {
			WorkerMetrics []struct {
				SessionID string
				PID       int
			}
		}
		api("GET", "/status", nil, &status)
		for _, worker := range status.WorkerMetrics {
			if worker.SessionID == session.ID {
				return worker.PID
			}
		}
		t.Fatal("session Worker missing")
		return 0
	}
	beforeDelegation := workerPID()
	delegatedRun := submit("delegation-check")
	waitRun(delegatedRun, "succeeded")
	if !delegationEvidence.Load() || !childToolEvidence.Load() || !reportEvidence.Load() || workerPID() != beforeDelegation {
		t.Fatalf("long delegation: prompt=%v, tool=%v, report=%v, worker unchanged=%v", delegationEvidence.Load(), childToolEvidence.Load(), reportEvidence.Load(), workerPID() == beforeDelegation)
	}
	var after int64
	childStates := map[string]int{}
	for {
		var events []store.Event
		api("GET", sessionPath+"/event-page?after="+strconv.FormatInt(after, 10), nil, &events)
		if len(events) == 0 {
			break
		}
		for _, event := range events {
			after = event.Sequence
			var result workerproto.Result
			if event.Kind != "agent_task" || json.Unmarshal(event.Data, &result) != nil || result.RunID != delegatedRun || result.Depth != 1 {
				continue
			}
			if result.Task == nil || result.Task.Title != longTask {
				t.Fatalf("invalid persisted child progress: %+v", result.Task)
			}
			childStates[result.Task.Status]++
		}
	}
	if childStates["started"] != 1 || childStates["succeeded"] != 1 || len(childStates) != 2 {
		t.Fatalf("incomplete child lifecycle: %v", childStates)
	}
	for _, prompt := range []string{"provider-error-ascii", "provider-error-unicode"} {
		waitRun(submit(prompt), "failed")
		waitRun(submit("shell-check"), "succeeded")
		if workerPID() != beforeDelegation {
			t.Fatal("upstream error replaced the Worker instead of failing only its run")
		}
	}
	waitRun(submit("long-finish-reason"), "failed")
	if workerPID() != beforeDelegation {
		t.Fatal("malformed upstream metadata replaced the Worker")
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
		t.Fatalf("shell child for %q did not start", prompt)
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
	var oldQueue struct{ Input store.QueuedInput }
	api("POST", sessionPath+"/messages", map[string]any{"content": "keep this old input paused"}, &oldQueue)
	if oldQueue.Input.ID == "" {
		t.Fatal("old task input was not queued")
	}
	api("POST", sessionPath+"/cancel", nil, nil)
	waitRun(runID, "interrupted")
	waitGone(child)
	findDiscoveredModel := func() string {
		var catalog struct{ Models []struct{ Key, ID string } }
		api("GET", "/models", nil, &catalog)
		for _, model := range catalog.Models {
			if model.ID == discoveredModel {
				return model.Key
			}
		}
		return ""
	}
	var discoveredKey string
	for deadline := time.Now().Add(6 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		if discoveredKey = findDiscoveredModel(); discoveredKey != "" {
			break
		}
	}
	if discoveredKey == "" {
		t.Fatal("the fixture's discovery-only model never appeared")
	}
	for _, action := range []string{"restart", "crash"} {
		prompt := "resume-" + action
		before := restartEvidence.Load()
		var interrupted string
		if action == "restart" {
			var run struct{ RunID string }
			api("POST", sessionPath+"/messages", map[string]string{"content": prompt, "selectionMode": "manual", "modelKey": discoveredKey}, &run)
			if run.RunID == "" {
				t.Fatal("the delayed-discovery task did not start")
			}
			interrupted = run.RunID
		} else {
			interrupted = submit(prompt)
		}
		select {
		case waiting := <-restartWaiting:
			if waiting != prompt {
				t.Fatalf("held wrong task: %s", waiting)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("task did not reach the post-tool model request")
		}
		if action == "crash" {
			info, err := instance.Read(filepath.Join(config, "state"))
			if err != nil {
				t.Fatal(err)
			}
			process, err := os.FindProcess(info.PID)
			if err != nil {
				t.Fatal(err)
			}
			if err := process.Kill(); err != nil {
				t.Fatal(err)
			}
			process.Release()
			if output, err := cli("start"); err != nil {
				t.Fatalf("start after crash: %v %s", err, output)
			}
		} else {
			delayInventory.Store(true)
			if output, err := cli("restart"); err != nil {
				t.Fatalf("restart active task: %v %s", err, output)
			}
		}
		waitRun(interrupted, "interrupted")
		if action == "restart" {
			select {
			case <-inventoryRequested:
			case <-time.After(3 * time.Second):
				t.Fatal("restarted server did not request the custom inventory")
			}
			if findDiscoveredModel() != "" || restartEvidence.Load() != before {
				t.Fatal("the interrupted task resumed before its saved model was discovered")
			}
			t.Log("Restart stayed ready without the custom inventory; now release discovery without a new message")
			releaseInventory()
		}
		deadline := time.Now().Add(15 * time.Second)
		var snapshot struct {
			Session  store.Session
			Messages []store.Message
		}
		for time.Now().Before(deadline) {
			api("GET", sessionPath+"/snapshot", nil, &snapshot)
			if !snapshot.Session.Executing && len(snapshot.Messages) > 0 && snapshot.Messages[len(snapshot.Messages)-1].Content == "Restart continuation verified." {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if restartEvidence.Load() != before+1 || snapshot.Session.Executing || len(snapshot.Messages) == 0 || snapshot.Messages[len(snapshot.Messages)-1].Content != "Restart continuation verified." {
			t.Fatalf("task did not resume exactly once: evidence=%d snapshot=%+v", restartEvidence.Load()-before, snapshot)
		}
		if !snapshot.Session.InputQueuePaused {
			t.Fatal("restart resumed the old input queue")
		}
		inputs := 0
		for _, message := range snapshot.Messages {
			if message.Role == "user" && message.Content == prompt {
				inputs++
			}
		}
		effects, err := os.ReadFile(filepath.Join(root, prompt+".effects"))
		if err != nil || strings.TrimSpace(string(effects)) != "completed" || inputs != 1 {
			t.Fatalf("restart replayed input or tool effects: inputs=%d effects=%q error=%v", inputs, effects, err)
		}
	}
	api("DELETE", sessionPath+"/inputs/"+oldQueue.Input.ID, nil, nil)
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
		if len(active.Command) > workerproto.MaxCommandPreviewBytes || !strings.Contains(active.Command, "Command preview truncated.") {
			t.Fatalf("invalid persisted long-command preview: %d bytes", len(active.Command))
		}
		if active.ExpiresAt != "" {
			t.Fatalf("background job acquired an implicit deadline: %+v", active)
		}
		api("GET", sessionPath+"/jobs/"+active.ID+"/output", nil, new(map[string]any))
		switch action {
		case "stop":
			api("POST", sessionPath+"/jobs/"+active.ID+"/stop", nil, nil)
			select {
			case <-completionStarted:
			case <-time.After(10 * time.Second):
				t.Fatal("background exit did not trigger an automatic continuation")
			}
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
