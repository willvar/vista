package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"vista/internal/agent"
	"vista/internal/agentconfig"
	"vista/internal/appdirs"
	"vista/internal/instance"
	modelcfg "vista/internal/model"
	"vista/internal/workerproto"
)

// Exercise producer and receiver together: nested task qualification must not
// invalidate a provider-accepted call ID before the tool can execute.
func TestNestedAgentProgressPassesWorkerBoundary(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	t.Setenv("HOME", root)
	config, err := appdirs.Config()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	if err := agentconfig.WriteSettings(config, agentconfig.Settings{DelegationDepth: 3}); err != nil {
		t.Fatal(err)
	}
	consumer, _ := newResultConsumerTest(t)
	consumer.stateDir, err = instance.Dir()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "evidence.txt")
	if err := os.WriteFile(path, []byte("nested evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) < 2 {
			http.Error(w, "invalid model request", http.StatusBadRequest)
			return
		}
		last := request.Messages[len(request.Messages)-1]
		delta, finish := map[string]any{"content": "nested work complete"}, "stop"
		name, id, input := "", strings.Repeat("d", 256), map[string]any{}
		if last.Role == "user" {
			level, _ := strconv.Atoi(strings.TrimPrefix(last.Content, "level-"))
			if level < 3 {
				name, input = "delegate", map[string]any{"task": fmt.Sprintf("level-%d", level+1)}
			} else {
				name, input = "read_file", map[string]any{"path": path}
			}
		}
		if name != "" {
			args, _ := json.Marshal(input)
			delta, finish = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(args)}}}}, "tool_calls"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer model.Close()
	deepTool := false
	var rejected error
	command := workerproto.Command{RunID: "run", Prompt: "level-0", Model: modelcfg.Model{Provider: "openai-chat", ID: "fixture", APIKey: "fixture", BaseURL: model.URL}}
	answer, err := agent.Run(context.Background(), command, func(result workerproto.Result) error {
		if result.Kind == "model_complete" {
			return nil // The Worker retains this locally until finalization.
		}
		if result.Kind == "tool" && result.Status == "started" || result.Kind == "artifact" && result.Status == "started" || result.Kind == "runtime_event" || result.Kind == "reply_checkpoint" {
			result.AckID = "committed"
		}
		if result.Tool != nil && result.Depth == 3 && result.Status == "succeeded" && len(result.Tool.CallID) > 1024 && strings.Contains(result.Content, "nested evidence") {
			deepTool = true
		}
		encoded, err := json.Marshal(result)
		if err != nil || len(encoded)+1 >= workerproto.MaxResultFrame {
			return fmt.Errorf("invalid encoded progress frame: %d bytes, %v", len(encoded), err)
		}
		err = consumer.consumeFrame(encoded)
		if err != nil {
			rejected = err
		}
		return err
	})
	if err != nil || rejected != nil || answer != "nested work complete" || !deepTool {
		t.Fatalf("nested progress: deepTool=%v answer=%q error=%v receiver=%v", deepTool, answer, err, rejected)
	}
	if err := consumeTestResult(t, consumer, workerproto.Result{RunID: "run", Status: "succeeded", Content: answer}); err != nil {
		t.Fatal(err)
	}
}
