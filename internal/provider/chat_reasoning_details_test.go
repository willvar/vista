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

func TestChatReasoningFragmentsMergeBeforeLimitsAndReplay(t *testing.T) {
	for _, providerID := range []string{"openrouter", "custom-relay"} {
		t.Run(providerID, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var input struct {
					Messages []map[string]json.RawMessage `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if requests == 1 {
					for range 1800 {
						fmt.Fprint(w, `data: {"choices":[{"delta":{"reasoning":"细节","reasoning_details":[{"type":"reasoning.text","id":"thought","index":0,"format":"test-v1","text":"细节"}]}}]}`+"\n\n")
					}
					fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"read-1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n")
					return
				}
				if len(input.Messages) != 3 {
					t.Errorf("continuation messages: %d", len(input.Messages))
					return
				}
				var details []struct {
					Text   string `json:"text"`
					ID     string `json:"id"`
					Format string `json:"format"`
				}
				if err := json.Unmarshal(input.Messages[1]["reasoning_details"], &details); err != nil || len(details) != 1 || details[0].Text != strings.Repeat("细节", 1800) || details[0].ID != "thought" || details[0].Format != "test-v1" {
					t.Errorf("replayed reasoning changed: blocks=%d err=%v", len(details), err)
				}
				fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Done"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: "openai-chat", ProviderID: providerID, ID: "fixture", BaseURL: server.URL + "/v1", APIKey: "test-key"}
			if providerID == "custom-relay" {
				model.Custom = true
				model.Authentication = "bearer"
			}
			messages := []ChatMessage{{Role: "user", Content: "Read the file"}}
			var visible strings.Builder
			first, err := ModelTurnStreamWithReasoning(context.Background(), model, messages, nil, nil, func(text string) error { visible.WriteString(text); return nil })
			if err != nil || len(first.ReasoningDetails) != 1 || len(first.Calls) != 1 || visible.String() != strings.Repeat("细节", 1800) {
				t.Fatalf("stream: blocks=%d visible=%d err=%v", len(first.ReasoningDetails), visible.Len(), err)
			}
			messages = append(messages, first.AssistantMessage(first.Text), ChatMessage{Role: "tool", ToolCallID: "read-1", Content: "file contents"})
			second, err := ModelTurn(context.Background(), model, messages, nil)
			if err != nil || second.Text != "Done" || requests != 2 {
				t.Fatalf("continuation: %+v %v requests=%d", second, err, requests)
			}
		})
	}
}

func TestChatReasoningLimitsCountBlocksAndDoNotMutateRejectedMerge(t *testing.T) {
	var details chatReasoningDetails
	for i := range maxChatReasoningDetailBlocks {
		if _, err := details.add(json.RawMessage(fmt.Sprintf(`[{"type":"reasoning.text","index":%d,"text":"x"}]`, i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := details.add(json.RawMessage(`[{"type":"reasoning.text","index":255,"text":"continued"}]`)); err != nil {
		t.Fatalf("continuing existing block at cap: %v", err)
	}
	if _, err := details.add(json.RawMessage(`[{"type":"reasoning.text","index":256,"text":"new"}]`)); err == nil || !strings.Contains(err.Error(), "256 independent blocks") {
		t.Fatalf("block cap error: %v", err)
	}
	var large chatReasoningDetails
	first, _ := json.Marshal([]map[string]string{{"type": "reasoning.text", "text": strings.Repeat("a", 60<<10)}})
	if _, err := large.add(first); err != nil {
		t.Fatal(err)
	}
	before, _ := large.replay()
	beforeBytes := large.bytes
	more, _ := json.Marshal([]map[string]string{{"type": "reasoning.text", "text": strings.Repeat("b", 8<<10)}})
	if _, err := large.add(more); err == nil || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("byte cap error: %v", err)
	}
	after, _ := large.replay()
	if !reflect.DeepEqual(before, after) || large.bytes != beforeBytes {
		t.Fatal("rejected merge changed accepted history")
	}
}

func TestChatReasoningMergePreservesIdentityOrderAndOpaqueBlocks(t *testing.T) {
	var details chatReasoningDetails
	for _, raw := range []string{
		`[{"type":"reasoning.text","index":0,"id":"a","signature":"sig-a","text":"A"}]`,
		`[{"type":"reasoning.summary","index":1,"id":"b","summary":"B"}]`,
		`[{"type":"reasoning.text","index":0,"id":"a","signature":"sig-a","text":"C"}]`,
		`[{"type":"reasoning.text","index":0,"id":"a","signature":"sig-b","text":"D"}]`,
		`[{"type":"reasoning.encrypted","id":"encrypted","data":"opaque-a"}]`,
		`[{"type":"reasoning.encrypted","id":"encrypted","data":"opaque-b"}]`,
	} {
		if _, err := details.add(json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	replay, err := details.replay()
	if err != nil || len(replay) != 5 {
		t.Fatalf("replay blocks: %s %v", replay, err)
	}
	if !strings.Contains(string(replay[0]), `"text":"AC"`) || !strings.Contains(string(replay[1]), `"summary":"B"`) || !strings.Contains(string(replay[2]), `"signature":"sig-b"`) || !strings.Contains(string(replay[3]), `"data":"opaque-a"`) || !strings.Contains(string(replay[4]), `"data":"opaque-b"`) {
		t.Fatalf("replay order/identity: %s", replay)
	}
	second, _ := details.replay()
	if !reflect.DeepEqual(replay, second) {
		t.Fatal("replaying modified retained blocks")
	}
}
