package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestInterjectionSupersedesPendingQuestion(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	state := filepath.Join(root, "vista", "state")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(state, "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Create(context.Background(), "session", "test"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn := calls.Add(1)
		var request struct {
			Messages []provider.ChatMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		var event any
		if turn == 1 {
			close(started)
			<-release
			event = mcpToolEvent("ask", "question", `{"questions":[{"question":"Which path?","options":[{"label":"/project"},{"label":"/other"}]}]}`)
		} else {
			last := request.Messages[len(request.Messages)-1]
			if last.Role != "user" || last.Content != "/project" {
				t.Errorf("interjection missing from next model decision: %+v", request.Messages)
			}
			result := request.Messages[len(request.Messages)-2]
			if result.Role != "tool" || result.ToolCallID != "ask" || !strings.Contains(result.Content, `"unanswered"`) {
				t.Errorf("superseded question lost its tool result: %+v", request.Messages)
			}
			event = map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": "I will use /project."}, "finish_reason": "stop"}}}
		}
		encoded, _ := json.Marshal(event)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", encoded)
	}))
	defer model.Close()
	inputs := make(chan workerproto.Command, 1)
	command := workerproto.Command{SessionID: "session", RunID: "run", Prompt: "work there", Model: modelcfg.Model{Provider: "openai-chat", ID: "fixture", BaseURL: model.URL, APIKey: "key"}}
	type outcome struct {
		answer    string
		err       error
		pending   bool
		delivered bool
	}
	done := make(chan outcome, 1)
	go func() {
		result := outcome{}
		result.answer, result.err = RunWithInputs(context.Background(), command, func(event workerproto.Result) error {
			if event.Kind == "question" {
				result.pending = true
			}
			if event.Kind == "input_delivered" {
				result.delivered = true
			}
			return nil
		}, inputs)
		done <- result
	}()
	<-started
	inputs <- workerproto.Command{Action: "interject", RunID: "run", InputID: "input", Prompt: "/project"}
	close(release)
	result := <-done
	if result.err != nil || result.answer != "I will use /project." || result.pending || !result.delivered || calls.Load() != 2 {
		t.Fatalf("interjection vs question: %+v calls=%d", result, calls.Load())
	}
}

func TestInterjectedInputReachesNextModelDecision(t *testing.T) {
	var calls atomic.Int32
	firstRequest := make(chan struct{})
	allowFirstResponse := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		turn := calls.Add(1)
		if turn == 1 {
			close(firstRequest)
			<-allowFirstResponse
		} else if turn == 2 {
			last := request.Messages[len(request.Messages)-1]
			if last.Role != "user" || last.Content != "interjected" {
				t.Errorf("interjection missing at model boundary: %+v", request.Messages)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer %d\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", turn)
	}))
	defer server.Close()
	inputs := make(chan workerproto.Command, 1)
	command := workerproto.Command{RunID: "run", Prompt: "original", Model: modelcfg.Model{Provider: "openai-chat", ID: "test", BaseURL: server.URL, APIKey: "key"}}
	type outcome struct {
		answer string
		err    error
	}
	done := make(chan outcome, 1)
	var delivered atomic.Int32
	var checkpoint workerproto.CompletedReply
	go func() {
		answer, err := RunWithInputs(context.Background(), command, func(result workerproto.Result) error {
			if result.Kind == "input_delivered" && result.InputID == "input" {
				delivered.Add(1)
				if result.Reply != nil {
					checkpoint = *result.Reply
				}
			}
			return nil
		}, inputs)
		done <- outcome{answer, err}
	}()
	<-firstRequest
	inputs <- workerproto.Command{Action: "interject", RunID: "run", InputID: "input", Prompt: "interjected"}
	close(allowFirstResponse)
	result := <-done
	if result.err != nil || result.answer != "answer 2" || calls.Load() != 2 || delivered.Load() != 1 || checkpoint.Turn != 1 || checkpoint.FullBytes != len("answer 1") || checkpoint.Content != "answer 1" {
		t.Fatalf("interjected run: %+v calls=%d deliveries=%d", result, calls.Load(), delivered.Load())
	}
}

func TestInterjectionSwitchesProtocolAtNextDecision(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer openai.Close()
	var anthropicCalls atomic.Int32
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthropicCalls.Add(1)
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "anthropic-key" {
			t.Errorf("wrong model route or credentials: %s", r.URL)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"switched answer\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer anthropic.Close()
	command := workerproto.Command{RunID: "switch-run", Prompt: "first", Model: modelcfg.Model{Provider: "openai-chat", ID: "openai-test", BaseURL: openai.URL, APIKey: "openai-key"}}
	inputs := make(chan workerproto.Command, 1)
	var usage []workerproto.ModelUse
	type outcome struct {
		answer string
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		answer, err := RunWithInputs(context.Background(), command, func(event workerproto.Result) error {
			if event.ModelUse != nil {
				usage = append(usage, *event.ModelUse)
			}
			return nil
		}, inputs)
		done <- outcome{answer, err}
	}()
	<-started
	inputs <- workerproto.Command{Action: "interject", RunID: "switch-run", InputID: "second", Prompt: "second", Model: modelcfg.Model{Provider: "anthropic", ID: "anthropic-test", BaseURL: anthropic.URL, APIKey: "anthropic-key"}}
	close(release)
	result := <-done
	if result.err != nil || result.answer != "switched answer" || anthropicCalls.Load() != 1 || len(usage) != 2 || usage[0].Provider != "openai-chat" || usage[1].Provider != "anthropic" {
		t.Fatalf("model switch failed: result=%+v calls=%d usage=%+v", result, anthropicCalls.Load(), usage)
	}
}
