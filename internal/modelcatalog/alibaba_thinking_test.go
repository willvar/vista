package modelcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"vista/internal/provider"
)

func TestAlibabaChatGenerationControls(t *testing.T) {
	for _, check := range []struct{ providerID, modelID string }{
		{"alibaba-coding-plan", "glm-5"},
		{"alibaba-token-plan", "glm-5.2"},
	} {
		t.Run(check.providerID, func(t *testing.T) {
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("Alibaba route/auth: %s, %q", r.URL.Path, r.Header.Get("Authorization"))
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				format, ok := body["response_format"].(map[string]any)
				schema, schemaOK := format["json_schema"].(map[string]any)
				if body["model"] != check.modelID || body["max_completion_tokens"] != float64(1000) || body["thinking_budget"] != float64(500) || body["repetition_penalty"] != 1.2 || body["reasoning_effort"] != "high" || !ok || format["type"] != "json_schema" || !schemaOK || schema["name"] != "answer" {
					t.Errorf("Alibaba generation controls: %+v", body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer endpoint.Close()
			root := t.TempDir()
			data := fmt.Sprintf(`{"default":"qwen","models":[{"key":"qwen","provider":"catalog","providerId":%q,"id":%q,"baseUrl":%q,"apiKey":"test-key","maxCompletionTokens":1000,"thinkingBudget":900,"repetitionPenalty":1.2,"reasoningEffort":"high","responseFormat":{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object"}}}}]}`, check.providerID, check.modelID, endpoint.URL+"/v1")
			if err := os.WriteFile(filepath.Join(root, "models.json"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			catalog, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			model, _, err := catalog.Resolve("")
			if err != nil {
				t.Fatal(err)
			}
			turn, err := provider.ModelTurn(context.Background(), model, []provider.ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if err != nil || turn.Text != "Done" {
				t.Fatalf("Alibaba Chat turn: %+v, err=%v", turn, err)
			}
		})
	}
}
