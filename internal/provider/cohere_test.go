package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
)

func TestCohereImageRequestShapeAndLimits(t *testing.T) {
	image := testImage(t)
	dataURL, err := imageDataURL(image)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v2/chat" || r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("Cohere image route or key: %s", r.URL)
		}
		var request struct {
			Messages []struct {
				Role    string           `json:"role"`
				Content []map[string]any `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request.Messages) != 1 || request.Messages[0].Role != "user" || len(request.Messages[0].Content) != 4 || request.Messages[0].Content[0]["type"] != "text" || request.Messages[0].Content[0]["text"] != "describe" || request.Messages[0].Content[1]["type"] != "image_url" || request.Messages[0].Content[1]["image_url"].(map[string]any)["url"] != dataURL {
			t.Errorf("Cohere image parts: %+v", request.Messages)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"content-delta\",\"delta\":{\"message\":{\"content\":{\"text\":\"seen\"}}}}\n\ndata: {\"type\":\"message-end\",\"delta\":{\"finish_reason\":\"COMPLETE\"}}\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "cohere", ID: "mock", BaseURL: server.URL + "/v2", APIKey: "key"}
	message := ChatMessage{Role: "user", Content: "describe", Images: []Image{image, image, image}}
	turn, err := ModelTurn(context.Background(), model, []ChatMessage{message}, nil)
	if err != nil || turn.Text != "seen" || calls != 1 {
		t.Fatalf("Cohere image turn: %+v, calls=%d, err=%v", turn, calls, err)
	}
	message.Images[0].Data = make([]byte, maxImageBytes+1)
	if _, err := ModelTurn(context.Background(), model, []ChatMessage{message}, nil); err == nil || !strings.Contains(err.Error(), "1 MiB") || calls != 1 {
		t.Fatalf("Cohere accepted oversized image request: %v, calls=%d", err, calls)
	}

}

func TestCohereStreamRejectsIncompleteAndUnsupportedOutput(t *testing.T) {
	for _, check := range []struct{ name, stream string }{
		{"missing finish", `data: {"type":"content-delta","delta":{"message":{"content":{"text":"partial"}}}}` + "\n\n"},
		{"unfinished tool", `data: {"type":"tool-call-start","delta":{"message":{"tool_calls":{"id":"call_1","function":{"name":"read_file","arguments":"{}"}}}}}` + "\n\n" + `data: {"type":"message-end","delta":{"finish_reason":"TOOL_CALL"}}` + "\n\n"},
		{"bad tool args", `data: {"type":"tool-call-start","delta":{"message":{"tool_calls":{"id":"call_1","function":{"name":"read_file","arguments":"{"}}}}}` + "\n\n" + `data: {"type":"tool-call-end"}` + "\n\n"},
		{"reasoning", `data: {"type":"content-start","delta":{"message":{"content":{"type":"thinking"}}}}` + "\n\n"},
		{"length finish", `data: {"type":"message-end","delta":{"finish_reason":"MAX_TOKENS"}}` + "\n\n"},
		{"negative usage", `data: {"type":"message-end","delta":{"finish_reason":"COMPLETE","usage":{"tokens":{"input_tokens":-1,"output_tokens":4}}}}` + "\n\n"},
		{"negative billed usage", `data: {"type":"message-end","delta":{"finish_reason":"COMPLETE","usage":{"billed_units":{"input_tokens":-1,"output_tokens":4}}}}` + "\n\n"},
	} {
		t.Run(check.name, func(t *testing.T) {
			if _, err := parseCohereStream(strings.NewReader(check.stream), nil); err == nil {
				t.Fatal("invalid Cohere stream accepted")
			}
		})
	}
}

func TestCohereStreamsThinkingSeparately(t *testing.T) {
	stream := `data: {"type":"content-start","delta":{"message":{"content":{"type":"thinking"}}}}` + "\n\n" +
		`data: {"type":"content-delta","delta":{"message":{"content":{"thinking":{"text":"Check inputs."}}}}}` + "\n\n" +
		`data: {"type":"content-end"}` + "\n\n" +
		`data: {"type":"content-delta","delta":{"message":{"content":{"text":"Answer."}}}}` + "\n\n" +
		`data: {"type":"message-end","delta":{"finish_reason":"COMPLETE"}}` + "\n\n"
	var text, reasoning strings.Builder
	turn, err := parseCohereStreamWithReasoning(strings.NewReader(stream), func(fragment string) error { _, err := text.WriteString(fragment); return err }, func(fragment string) error { _, err := reasoning.WriteString(fragment); return err })
	if err != nil || turn.Text != "Answer." || turn.FinishReason != "COMPLETE" || text.String() != "Answer." || reasoning.String() != "Check inputs." {
		t.Fatalf("Cohere thinking: turn=%+v, text=%q, reasoning=%q, err=%v", turn, text.String(), reasoning.String(), err)
	}
	abort := errors.New("reasoning persistence failed")
	if _, err := parseCohereStreamWithReasoning(strings.NewReader(stream), nil, func(string) error { return abort }); !errors.Is(err, abort) {
		t.Fatalf("reasoning sink error was not propagated: %v", err)
	}
}

func TestCohereBoundsCumulativeThinking(t *testing.T) {
	delta := strings.Repeat("r", 90<<10)
	var stream strings.Builder
	for range 3 {
		item, err := json.Marshal(map[string]any{"type": "content-delta", "delta": map[string]any{"message": map[string]any{"content": map[string]any{"thinking": delta}}}})
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&stream, "data: %s\n\n", item)
	}
	stream.WriteString(`data: {"type":"message-end","delta":{"finish_reason":"COMPLETE"}}` + "\n\n")
	if _, err := parseCohereStreamWithReasoning(strings.NewReader(stream.String()), nil, func(string) error { return nil }); err == nil || !strings.Contains(err.Error(), "reasoning exceeds") {
		t.Fatalf("oversized Cohere reasoning accepted: %v", err)
	}
}

func TestCoherePreservesFinishAndReportedUsage(t *testing.T) {
	stream := `data: {"type":"content-delta","delta":{"message":{"content":{"text":"answer"}}}}` + "\n\n" +
		`data: {"type":"message-end","delta":{"finish_reason":"COMPLETE","usage":{"tokens":{"input_tokens":71,"output_tokens":26},"billed_units":{"input_tokens":5,"output_tokens":26}}}}` + "\n\n"
	turn, err := parseCohereStream(strings.NewReader(stream), nil)
	if err != nil || turn.Text != "answer" || turn.FinishReason != "COMPLETE" || turn.Usage == nil || *turn.Usage != (ModelUsage{InputTokens: 71, OutputTokens: 26}) {
		t.Fatalf("Cohere usage: %+v, err=%v", turn, err)
	}
}

func TestCohereStreamBoundsCumulativeText(t *testing.T) {
	delta := strings.Repeat("x", 90<<10)
	encoded, err := json.Marshal(map[string]any{"type": "content-delta", "delta": map[string]any{"message": map[string]any{"content": map[string]any{"text": delta}}}})
	if err != nil {
		t.Fatal(err)
	}
	stream := strings.Repeat(fmt.Sprintf("data: %s\n\n", encoded), 3)
	if _, err := parseCohereStream(strings.NewReader(stream), nil); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized Cohere reply accepted: %v", err)
	}
}
