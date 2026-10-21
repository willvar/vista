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

func TestVercelGatewayV3ImageFilePartsAndLimit(t *testing.T) {
	image := testImage(t)
	dataURL, err := imageDataURL(image)
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v3/ai/language-model" || r.Header.Get("ai-language-model-specification-version") != "3" || r.Header.Get("ai-language-model-id") != "test-model" {
			t.Errorf("Gateway image route or model: %s, headers=%v", r.URL, r.Header)
		}
		var body struct {
			Prompt []struct {
				Role    string           `json:"role"`
				Content []map[string]any `json:"content"`
			} `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		} else if len(body.Prompt) != 1 || body.Prompt[0].Role != "user" || len(body.Prompt[0].Content) != 2 {
			t.Errorf("Gateway image prompt: %+v", body.Prompt)
		} else {
			parts := body.Prompt[0].Content
			if parts[0]["type"] != "text" || parts[0]["text"] != "describe" || parts[1]["type"] != "file" || parts[1]["mediaType"] != "image/png" || parts[1]["data"] != dataURL {
				t.Errorf("Gateway v3 file parts: %+v", parts)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"text-delta\",\"id\":\"text_1\",\"delta\":\"seen\"}\n\ndata: {\"type\":\"finish\",\"finishReason\":{\"unified\":\"stop\"}}\n\n")
	}))
	defer endpoint.Close()
	model := modelcfg.Model{Provider: "vercel-gateway", ID: "test-model", APIKey: "key", BaseURL: endpoint.URL + "/v3/ai"}
	message := ChatMessage{Role: "user", Content: "describe", Images: []Image{image}}
	turn, err := ModelTurn(context.Background(), model, []ChatMessage{message}, nil)
	if err != nil || turn.Text != "seen" || requests != 1 {
		t.Fatalf("Gateway image turn: %+v, requests=%d, err=%v", turn, requests, err)
	}
	message.Images[0].Data = make([]byte, maxImageBytes+1)
	if _, err := ModelTurn(context.Background(), model, []ChatMessage{message}, nil); err == nil || !strings.Contains(err.Error(), "1 MiB") || requests != 1 {
		t.Fatalf("Gateway accepted oversized image request: %v, requests=%d", err, requests)
	}
}

func TestVercelGatewayStreamRejectsIncompleteAndErrorOutput(t *testing.T) {
	for _, check := range []struct{ name, stream string }{
		{"missing finish", `data: {"type":"text-delta","id":"text_1","delta":"partial"}` + "\n\n"},
		{"unfinished tool", `data: {"type":"tool-input-start","id":"call_1","toolName":"read_file"}` + "\n\n" + `data: {"type":"finish","finishReason":{"unified":"tool-calls"}}` + "\n\n"},
		{"error event", `data: {"type":"error","error":{"message":"failed"}}` + "\n\n"},
		{"length finish", `data: {"type":"finish","finishReason":{"unified":"length"}}` + "\n\n"},
		{"provider tool", `data: {"type":"tool-call","toolCallId":"call_1","toolName":"write_file","input":"{}","providerExecuted":true}` + "\n\n"},
	} {
		t.Run(check.name, func(t *testing.T) {
			if _, err := parseVercelGatewayStream(strings.NewReader(check.stream), nil); err == nil {
				t.Fatal("invalid gateway stream accepted")
			}
		})
	}
}

func TestVercelGatewayStreamBoundsCumulativeText(t *testing.T) {
	delta := strings.Repeat("x", 90<<10)
	var stream strings.Builder
	for range 3 {
		item, err := json.Marshal(map[string]any{"type": "text-delta", "id": "text_1", "delta": delta})
		if err != nil {
			t.Fatal(err)
		}
		stream.WriteString("data: ")
		stream.Write(item)
		stream.WriteString("\n\n")
	}
	stream.WriteString(`data: {"type":"finish","finishReason":{"unified":"stop"}}` + "\n\n")
	if _, err := parseVercelGatewayStream(strings.NewReader(stream.String()), nil); err == nil || !strings.Contains(err.Error(), "exceeds limits") {
		t.Fatalf("oversized gateway text accepted: %v", err)
	}
}

func TestVercelGatewayStreamsReasoningAndTextSeparately(t *testing.T) {
	stream := `data: {"type":"reasoning-start","id":"reason_1"}` + "\n\n" +
		`data: {"type":"reasoning-delta","id":"reason_1","delta":"Check the inputs."}` + "\n\n" +
		`data: {"type":"reasoning-end","id":"reason_1"}` + "\n\n" +
		`data: {"type":"text-delta","id":"text_1","delta":"Result."}` + "\n\n" +
		`data: {"type":"finish","finishReason":{"unified":"stop"}}` + "\n\n"
	var text, reasoning strings.Builder
	turn, err := parseVercelGatewayStreamWithReasoning(strings.NewReader(stream), func(fragment string) error { _, err := text.WriteString(fragment); return err }, func(fragment string) error { _, err := reasoning.WriteString(fragment); return err })
	if err != nil || turn.Text != "Result." || turn.FinishReason != "stop" || text.String() != "Result." || reasoning.String() != "Check the inputs." {
		t.Fatalf("gateway streams: turn=%+v text=%q reasoning=%q err=%v", turn, text.String(), reasoning.String(), err)
	}
	if _, err := parseVercelGatewayStream(strings.NewReader(stream), nil); err == nil || !strings.Contains(err.Error(), "requires a sink") {
		t.Fatalf("reasoning was silently dropped: %v", err)
	}
	abort := errors.New("reasoning persistence failed")
	if _, err := parseVercelGatewayStreamWithReasoning(strings.NewReader(stream), nil, func(string) error { return abort }); !errors.Is(err, abort) {
		t.Fatalf("reasoning sink error was not propagated: %v", err)
	}
}

func TestVercelGatewayV3FinishUsage(t *testing.T) {
	stream := `data: {"type":"text-delta","id":"text_1","delta":"done"}` + "\n\n" +
		`data: {"type":"finish","finishReason":{"unified":"stop"},"usage":{"inputTokens":{"total":120,"cacheRead":30},"outputTokens":{"total":40,"reasoning":12}}}` + "\n\n"
	turn, err := parseVercelGatewayStream(strings.NewReader(stream), nil)
	if err != nil || turn.Text != "done" || turn.Usage == nil || *turn.Usage != (ModelUsage{InputTokens: 120, OutputTokens: 40, CacheReadTokens: 30, ReasoningTokens: 12}) {
		t.Fatalf("Gateway v3 finish usage: turn=%+v err=%v", turn, err)
	}
	for _, raw := range []string{
		`{"inputTokens":{"total":-1},"outputTokens":{"total":40}}`,
		`{"inputTokens":{"total":120},"outputTokens":{"total":1000000001}}`,
		`{"inputTokens":{"total":"120"},"outputTokens":{"total":40}}`,
	} {
		bad := `data: {"type":"finish","finishReason":{"unified":"stop"},"usage":` + raw + `}` + "\n\n"
		if _, err := parseVercelGatewayStream(strings.NewReader(bad), nil); err == nil {
			t.Fatalf("invalid Gateway usage accepted: %s", raw)
		}
	}
}

func TestVercelGatewayBoundsCumulativeReasoning(t *testing.T) {
	delta := strings.Repeat("x", 90<<10)
	var stream strings.Builder
	for range 3 {
		item, err := json.Marshal(map[string]any{"type": "reasoning-delta", "id": "reason_1", "delta": delta})
		if err != nil {
			t.Fatal(err)
		}
		stream.WriteString("data: ")
		stream.Write(item)
		stream.WriteString("\n\n")
	}
	stream.WriteString(`data: {"type":"finish","finishReason":{"unified":"stop"}}` + "\n\n")
	if _, err := parseVercelGatewayStreamWithReasoning(strings.NewReader(stream.String()), nil, func(string) error { return nil }); err == nil || !strings.Contains(err.Error(), "reasoning delta exceeds limits") {
		t.Fatalf("oversized reasoning was accepted: %v", err)
	}
}
