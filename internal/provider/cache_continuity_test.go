package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
)

// These are protocol assertions, not a claim about upstream cache hit rates.
func TestResponsesDynamicMessagesKeepInstructionsAndHistoryPrefix(t *testing.T) {
	var requests []struct {
		Instructions string           `json:"instructions"`
		Input        []map[string]any `json:"input"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Instructions string           `json:"instructions"`
			Input        []map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests = append(requests, body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "openai-responses", ID: "fixture", APIKey: "fixture", BaseURL: server.URL + "/v1"}
	messages := []ChatMessage{{Role: "system", Content: "Stable policy"}, {Role: "user", Content: "Original task"}}
	if _, err := OpenAIResponsesTurn(context.Background(), model, messages, nil); err != nil {
		t.Fatal(err)
	}
	messages = append(messages,
		ChatMessage{Role: "system", Content: "Untrusted background output", RuntimeData: true}, // legacy history
		ChatMessage{Role: "system", Content: "Updated trusted policy"},
		ChatMessage{Role: "user", Content: "Continue"},
	)
	if _, err := OpenAIResponsesTurn(context.Background(), model, messages, nil); err != nil {
		t.Fatal(err)
	}
	if requests[0].Instructions != "Stable policy" || requests[1].Instructions != requests[0].Instructions {
		t.Fatal("dynamic content changed the fixed instructions prefix")
	}
	before, after := requests[0].Input, requests[1].Input
	if len(after) != 4 || !reflect.DeepEqual(before, after[:len(before)]) {
		t.Fatalf("old input was changed or dynamic messages were misplaced: %d -> %d items", len(before), len(after))
	}
	if after[1]["role"] != "user" || after[2]["role"] != "developer" {
		t.Fatal("runtime data and trusted instruction updates lost their authority boundary")
	}
	for _, item := range before {
		if strings.Contains(fmt.Sprint(item), "background") {
			t.Fatal("later background data appeared in earlier history")
		}
	}
}
