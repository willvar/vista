//go:build linux

package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
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

	modelcfg "vista/internal/model"
	"vista/internal/platform"
	"vista/internal/resources"
	"vista/internal/workerproto"
)

func TestWorkerDetachedTaskCanPersistToolsAfterNextForegroundRun(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "evidence.txt")
	if err := os.WriteFile(path, []byte("background evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var releaseOnce sync.Once
	finishSearch := func() { releaseOnce.Do(func() { close(release) }) }
	defer finishSearch()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer renewed-") {
			t.Error("worker used stale credential")
		}
		var request struct {
			Messages []struct {
				Role    string
				Content json.RawMessage
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		prompt := string(request.Messages[1].Content)
		w.Header().Set("Content-Type", "text/event-stream")
		if prompt == `"child"` && len(request.Messages) == 2 {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			arguments, _ := json.Marshal(map[string]any{"path": path})
			call, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "read", "type": "function", "function": map[string]any{"name": "read_file", "arguments": string(arguments)}}}}, "finish_reason": "tool_calls"}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", call)
		} else if prompt == `"parent"` && len(request.Messages) == 2 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"child\",\"type\":\"function\",\"function\":{\"name\":\"delegate\",\"arguments\":\"{\\\"task\\\":\\\"child\\\",\\\"join\\\":\\\"detached\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
		} else {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		}
	}))
	defer func() { finishSearch(); server.Close() }()
	// Release blocked HTTP work even when an assertion fails before cleanup.
	t.Cleanup(finishSearch)
	input, results := startWorkerFixture(t)
	command := workerproto.Command{RunID: "first", Prompt: "parent", Model: modelcfg.Model{Provider: "openai-chat", ID: "fixture", APIKey: "expired", BaseURL: server.URL, Key: "saved", CredentialRef: strings.Repeat("a", 64)}}
	if err := input.Encode(command); err != nil {
		t.Fatal(err)
	}
	formal, childTool := 0, false
	credentialRequests := 0
	deadline := time.After(10 * time.Second)
	for {
		select {
		case result, ok := <-results:
			if !ok {
				t.Fatal("Worker exited")
			}
			if result.AckID != "" {
				ack := workerproto.Command{Action: "ack", RunID: result.RunID, AckID: result.AckID}
				if result.Kind == "model_credentials" {
					if result.Credential == nil || result.Credential.Key != "saved" {
						t.Fatal("missing credential reference")
					}
					credentialRequests++
					ack.Model.APIKey = fmt.Sprintf("renewed-%d", credentialRequests)
				}
				if err := input.Encode(ack); err != nil {
					t.Fatal(err)
				}
			}
			if result.Kind == "" {
				if result.Status != "succeeded" {
					t.Fatalf("foreground failed: %+v", result)
				}
				formal++
				if formal == 1 {
					command.RunID, command.Prompt = "second", "next"
					if err := input.Encode(command); err != nil {
						t.Fatal(err)
					}
				} else {
					finishSearch()
				}
			}
			if result.Kind == "tool" && result.TaskID == "main/child" && result.Status == "succeeded" {
				childTool = true
				if !strings.Contains(result.Content, "background evidence") {
					t.Fatalf("child tool result: %+v", result)
				}
			}
			if result.Task != nil && result.Task.ID == "main/child" && result.Task.Status != "started" {
				if credentialRequests < 5 || formal != 2 || !childTool || result.RunID != "first" || result.Task.Status != "succeeded" || result.Task.Report == nil {
					t.Fatalf("detached completion: formal=%d tool=%t %+v", formal, childTool, result)
				}
				return
			}
		case <-deadline:
			t.Fatal("detached task did not finish")
		}
	}
}

func startWorkerFixture(t *testing.T, configRoots ...string) (*json.Encoder, <-chan workerproto.Result) {
	input, results, _ := startWorkerFixtureProcess(t, configRoots...)
	return input, results
}

func startWorkerFixtureProcess(t *testing.T, configRoots ...string) (*json.Encoder, <-chan workerproto.Result, func()) {
	t.Helper()
	root := t.TempDir()
	if len(configRoots) > 0 {
		root = configRoots[0]
	}
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
	stopped := make(chan struct{})
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			close(stopped)
			_ = input.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
	}
	t.Cleanup(stop)
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), workerproto.MaxResultFrame)
	if !scanner.Scan() || scanner.Text() != "READY" {
		t.Fatalf("worker not ready: %q stderr=%s", scanner.Text(), stderr.String())
	}
	results := make(chan workerproto.Result, 32)
	go func() {
		defer close(results)
		for scanner.Scan() {
			var result workerproto.Result
			if json.Unmarshal(scanner.Bytes(), &result) != nil {
				continue
			}
			if result.Kind == "media_admission" {
				_ = json.NewEncoder(input).Encode(workerproto.Command{Action: "ack", AckID: result.AckID, RunID: result.RunID})
				continue
			}
			select {
			case results <- result:
			case <-stopped:
				return
			}
		}
	}()
	return json.NewEncoder(input), results, stop
}

func TestWorkerSendsCurrentAndInterjectedImagesWithoutReplayingHistory(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lN8AAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, make([]byte, (1<<20)-len(data))...)
	root := t.TempDir()
	library := resources.Library{StateDir: filepath.Join(root, "vista", "state")}
	unlock, err := library.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	hash, size, err := library.Put(context.Background(), bytes.NewReader(data))
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	image := workerproto.Image{Hash: hash, MediaType: "image/png", Bytes: size}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		count := 0
		for _, message := range request.Messages {
			var parts []struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(message.Content, &parts) == nil {
				for _, part := range parts {
					if part.Type == "image_url" {
						count++
					}
				}
			}
		}
		if count != 7 {
			t.Errorf("model received %d images, want only 4 current + 3 interjection", count)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"seen\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	input, results := startWorkerFixture(t, root)
	command := workerproto.Command{
		RunID: "images", Prompt: "Describe all images", Images: []workerproto.Image{image, image, image, image},
		History: []workerproto.Message{{Role: "user", Content: "Previous images", Images: []workerproto.Image{image, image, image}}},
		Model:   modelcfg.Model{Provider: "openai-chat", ID: "test", APIKey: "key", BaseURL: server.URL, Context: 32768},
	}
	if err := input.Encode(command); err != nil {
		t.Fatal(err)
	}
	inserted := false
	deadline := time.After(10 * time.Second)
	for {
		select {
		case result, ok := <-results:
			if !ok {
				t.Fatal("worker exited before processing all images")
			}
			if result.AckID != "" {
				ack := workerproto.Command{Action: "ack", RunID: result.RunID, AckID: result.AckID}
				if result.Kind == "input_poll" && !inserted {
					ack.Input = &workerproto.Command{RunID: command.RunID, InputID: "interjection", Images: []workerproto.Image{image, image, image}}
					inserted = true
				}
				if err := input.Encode(ack); err != nil {
					t.Fatal(err)
				}
			}
			if result.RunID == command.RunID && result.Kind == "" {
				if !inserted || result.Status != "succeeded" || result.Content != "seen" {
					t.Fatalf("image execution: %+v, inserted=%t", result, inserted)
				}
				return
			}
		case <-deadline:
			t.Fatal("worker did not complete image execution")
		}
	}
}

func TestWorkerPollCarriesCompletedReplyBeforeFollowUp(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []workerproto.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		requests++
		if requests == 2 {
			tail := request.Messages[len(request.Messages)-2:]
			if tail[0].Role != "assistant" || tail[0].Content != "answer 1" || tail[1].Role != "user" || tail[1].Content != "follow-up" {
				t.Errorf("follow-up lost previous reply: %+v", tail)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer %d\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", requests)
	}))
	defer server.Close()
	input, results := startWorkerFixture(t)
	command := workerproto.Command{RunID: "reply-boundary", Prompt: "original", Model: modelcfg.Model{Provider: "openai-chat", ID: "test", APIKey: "key", BaseURL: server.URL}}
	if err := input.Encode(command); err != nil {
		t.Fatal(err)
	}
	inserted := false
	deadline := time.After(5 * time.Second)
	for {
		select {
		case result, ok := <-results:
			if !ok {
				t.Fatal("worker exited before completing follow-up")
			}
			if result.AckID != "" {
				ack := workerproto.Command{Action: "ack", RunID: result.RunID, AckID: result.AckID}
				if result.Kind == "input_poll" && result.Reply != nil && !inserted {
					if result.Reply.Turn != 1 || result.Reply.FullBytes != len("answer 1") || result.Reply.Content != "answer 1" {
						t.Fatalf("wrong completed reply in poll: %+v", result.Reply)
					}
					ack.Input = &workerproto.Command{RunID: command.RunID, InputID: "follow-up", Prompt: "follow-up"}
					inserted = true
				}
				if err := input.Encode(ack); err != nil {
					t.Fatal(err)
				}
			}
			if result.RunID == command.RunID && result.Kind == "" {
				if !inserted || result.Status != "succeeded" || result.Content != "answer 2" || result.Turn != 2 {
					t.Fatalf("follow-up execution: %+v, inserted=%t", result, inserted)
				}
				return
			}
		case <-deadline:
			t.Fatal("worker did not complete follow-up")
		}
	}
}

func TestWorkerCommandStreamEOFAndMalformedJSON(t *testing.T) {
	for _, test := range []struct{ name, input, errorText string }{
		{name: "EOF"},
		{name: "malformed", input: "{", errorText: "invalid worker command: unexpected EOF"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkerAckHelper$")
			cmd.Env = append(os.Environ(), "VISTA_WORKER_ACK_HELPER=1", "XDG_CONFIG_HOME="+t.TempDir())
			cmd.Stdin = strings.NewReader(test.input)
			output, err := cmd.CombinedOutput()
			if test.errorText == "" {
				if err != nil {
					t.Fatalf("clean EOF: %v %s", err, output)
				}
			} else if err == nil || !strings.Contains(string(output), test.errorText) {
				t.Fatalf("malformed stream: %v %s", err, output)
			}
		})
	}
}

func TestWorkerRejectsUnconfiguredAndUnknownProtocols(t *testing.T) {
	input, results := startWorkerFixture(t)
	for _, test := range []struct {
		protocol string
		message  string
	}{
		{"", "No model is configured."},
		{"unknown", "Unsupported model provider"},
	} {
		runID := "rejected-" + test.protocol
		if err := input.Encode(workerproto.Command{RunID: runID, Model: modelcfg.Model{Provider: test.protocol}}); err != nil {
			t.Fatal(err)
		}
		deadline := time.After(5 * time.Second)
		for {
			select {
			case result, ok := <-results:
				if !ok {
					t.Fatal("worker exited before rejection")
				}
				if result.RunID != runID || result.Kind != "" {
					continue
				}
				if result.Status != "failed" || !strings.HasPrefix(result.Content, test.message) {
					t.Fatalf("protocol %q result = %+v", test.protocol, result)
				}
				goto completed
			case <-deadline:
				t.Fatal("worker did not reject protocol")
			}
		}
	completed:
	}
}

func TestWorkerCancelReleasesPendingInputAck(t *testing.T) {
	input, results := startWorkerFixture(t)
	command := workerproto.Command{RunID: "cancel-pending", Prompt: "Hi", Model: modelcfg.Model{Provider: "openai-chat", ID: "fixture", APIKey: "key", BaseURL: "http://127.0.0.1:1"}}
	if err := input.Encode(command); err != nil {
		t.Fatal(err)
	}
	canceled := false
	deadline := time.After(5 * time.Second)
	for {
		select {
		case result, ok := <-results:
			if !ok {
				t.Fatal("worker exited before cancellation completed")
			}
			if result.Kind == "agent_task" && result.AckID != "" {
				if err := input.Encode(workerproto.Command{Action: "ack", RunID: result.RunID, AckID: result.AckID}); err != nil {
					t.Fatal(err)
				}
			}
			if result.Kind == "input_poll" {
				if result.AckID == "" {
					t.Fatal("input poll has no acknowledgment ID")
				}
				if err := input.Encode(workerproto.Command{Action: "cancel", RunID: command.RunID}); err != nil {
					t.Fatal(err)
				}
				canceled = true
			}
			if result.RunID == command.RunID && result.Kind == "" {
				if !canceled || result.Status != "interrupted" || result.Content != "Cancelled by user" || result.FullBytes != 0 || result.Turn != 0 || result.Pending != nil {
					t.Fatalf("canceled result = %+v, cancel sent = %t", result, canceled)
				}
				return
			}
		case <-deadline:
			t.Fatal("worker did not release the pending input acknowledgment on cancel")
		}
	}
}
