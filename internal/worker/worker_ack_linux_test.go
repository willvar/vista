//go:build linux

package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"vista/internal/modelcatalog"
	"vista/internal/platform"
	"vista/internal/workerproto"
)

type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func TestWorkerAckHelper(t *testing.T) {
	if os.Getenv("VISTA_WORKER_ACK_HELPER") != "1" {
		return
	}
	if err := Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestDiscoveredVLLMWaitsForToolAckAndContinues(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "created.txt")
	requests := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			return
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"local-model","owned_by":"vllm"}]}`)
			return
		}
		var request struct {
			Tools    []json.RawMessage
			Messages []struct {
				Role    string
				Content json.RawMessage
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request.Tools) == 0 {
			t.Error("vLLM tool schemas disabled")
		}
		requests++
		if requests > 1 && !strings.Contains(string(request.Messages[len(request.Messages)-1].Content), "created") {
			t.Error("tool result missing from continuation")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			args, _ := json.Marshal(map[string]string{"path": path, "expectedSha256": "missing", "content": "created"})
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_write", "type": "function", "function": map[string]string{"name": "write_file", "arguments": string(args)}}}}, "finish_reason": "tool_calls"}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer model.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestWorkerAckHelper$")
	cmd.Env = append(os.Environ(), "VISTA_WORKER_ACK_HELPER=1", "XDG_CONFIG_HOME="+root)
	platform.AttachToParent(cmd)
	var stderr lockedBuffer
	cmd.Stderr = &stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = input.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() || scanner.Text() != "READY" {
		t.Fatalf("Worker not ready: %q stderr=%s", scanner.Text(), stderr.String())
	}
	results := make(chan workerproto.Result, 32)
	go func() {
		for scanner.Scan() {
			var result workerproto.Result
			if json.Unmarshal(scanner.Bytes(), &result) == nil {
				results <- result
			}
		}
		close(results)
	}()
	catalog, err := modelcatalog.ChangeConnection(context.Background(), modelcatalog.Catalog{}, false, http.MethodPost, modelcatalog.ConnectionInput{Provider: "catalog", ProviderID: "vllm", BaseURL: model.URL + "/v1", CredentialMode: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if err := modelcatalog.ResolveCatalog(&catalog); err != nil {
		t.Fatal(err)
	}
	selected, _, err := catalog.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	command := workerproto.Command{RunID: "ack-run", Prompt: "write a file", Model: selected}
	if err := json.NewEncoder(input).Encode(command); err != nil {
		t.Fatal(err)
	}
	var ackID string
	deadline := time.After(5 * time.Second)
	for ackID == "" {
		select {
		case result, ok := <-results:
			if !ok {
				t.Fatalf("Worker closed before tool start: %s", stderr.String())
			}
			if result.Kind == "input_poll" || result.Kind == "agent_task" && result.AckID != "" {
				if err := json.NewEncoder(input).Encode(workerproto.Command{Action: "ack", RunID: result.RunID, AckID: result.AckID}); err != nil {
					t.Fatal(err)
				}
			}
			if result.Kind == "tool" && result.Status == "started" {
				ackID = result.AckID
			}
		case <-deadline:
			t.Fatalf("Worker did not report tool start: %s", stderr.String())
		}
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file appeared before tool start was acknowledged: %v", err)
	}
	if err := json.NewEncoder(input).Encode(workerproto.Command{Action: "ack", RunID: "ack-run", AckID: ackID}); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case result, ok := <-results:
			if result.Kind == "input_poll" || result.Kind == "agent_task" && result.AckID != "" {
				if err := json.NewEncoder(input).Encode(workerproto.Command{Action: "ack", RunID: result.RunID, AckID: result.AckID}); err != nil {
					t.Fatal(err)
				}
			}
			if !ok {
				t.Fatalf("Worker closed before completion: %s", stderr.String())
			}
			if result.RunID == "ack-run" && result.Kind == "" && result.Status == "succeeded" {
				data, err := os.ReadFile(path)
				if err != nil || strings.TrimSpace(string(data)) != "created" {
					t.Fatalf("file after ACK: %q %v", data, err)
				}
				return
			}
		case <-deadline:
			t.Fatalf("Worker did not complete after ACK: %s", stderr.String())
		}
	}
}
