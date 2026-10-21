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
	"testing"

	"vista/internal/agentconfig"
	modelcfg "vista/internal/model"
	"vista/internal/workerproto"
)

func TestGlobalInstructionsApplyOnNextRun(t *testing.T) {
	if len(systemPrompt) >= 1024 {
		t.Fatalf("built-in prompt grew beyond 1 KiB: %d bytes", len(systemPrompt))
	}
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	configDir := filepath.Join(configRoot, "vista")
	if err := os.Mkdir(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(configDir, "state")
	if err := os.Mkdir(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := agentconfig.Write(stateDir, "Obsolete state-directory instructions."); err != nil {
		t.Fatal(err)
	}
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if len(input.Messages) == 0 || input.Messages[0].Role != "system" {
			t.Errorf("system message missing: %+v", input.Messages)
		} else {
			seen = append(seen, input.Messages[0].Content)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	runOnce := func() {
		t.Helper()
		command := workerproto.Command{RunID: "run-1", Prompt: "Hi", Model: modelcfg.Model{Provider: "openai-chat", ID: "test", BaseURL: upstream.URL, APIKey: "test"}}
		if answer, err := Run(context.Background(), command, func(workerproto.Result) error { return nil }); err != nil || answer != "Hi" {
			t.Fatalf("agent run: %q, %v", answer, err)
		}
	}
	runOnce()
	if err := agentconfig.Write(configDir, "Reply in Chinese."); err != nil {
		t.Fatal(err)
	}
	runOnce()
	if len(seen) != 2 || strings.Contains(seen[0], "Reply in Chinese.") || !strings.Contains(seen[1], "User's global instructions:\nReply in Chinese.") {
		t.Fatalf("instructions were not applied on the next run: %+v", seen)
	}
	for _, prompt := range seen {
		if strings.Contains(prompt, "Obsolete state-directory instructions.") {
			t.Fatal("instructions were loaded from the state directory")
		}
	}
}
