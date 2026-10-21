package provider

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
)

func TestSSEDataAcceptsOptionalSpace(t *testing.T) {
	for _, check := range []struct {
		line, data string
		ok         bool
	}{
		{"data:hello", "hello", true},
		{"data: hello", "hello", true},
		{"data:  hello", " hello", true},
		{"data:", "", true},
		{"data:null", "null", true},
		{"data: : keepalive", ": keepalive", true},
		{": comment", "", false},
		{"event: message", "", false},
	} {
		data, ok := sseDataLine(check.line)
		if data != check.data || ok != check.ok {
			t.Errorf("SSE line %q: data=%q ok=%v", check.line, data, ok)
		}
	}
}

func TestSSEKeepaliveAppliesToCompleteEvent(t *testing.T) {
	input := "data:null\n\ndata:: keepalive\n\ndata:null\ndata:{}\n\n"
	scanner := bufio.NewScanner(strings.NewReader(input))
	scanner.Buffer(make([]byte, 4096), maxEventBytes)
	data, ok, err := nextSSEData(scanner, maxEventBytes)
	if err != nil || !ok || data != "null\n{}" {
		t.Fatalf("joined SSE event: data=%q ok=%v err=%v", data, ok, err)
	}
}

func TestSSEEmptyFirstDataFieldKeepsNewline(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("data:\ndata:{}\n\n"))
	scanner.Buffer(make([]byte, 4096), maxEventBytes)
	data, ok, err := nextSSEData(scanner, maxEventBytes)
	if err != nil || !ok || data != "\n{}" {
		t.Fatalf("joined SSE event: data=%q ok=%v err=%v", data, ok, err)
	}
}

var pinnedSSEStreams = []struct {
	name, protocol, stream, want string
}{
	{"Chat", "openai-chat", `data:{"choices":[{"delta":{"content":"chat"},"finish_reason":"stop"}]}` + "\n\ndata:[DONE]\n\n", "chat"},
	{"Responses", "openai-responses", `data:{"type":"response.output_text.delta","item_id":"msg","delta":"responses"}` + "\n\n" + `data:{"type":"response.completed","response":{"status":"completed","output":[]}}` + "\n\n", "responses"},
	{"Anthropic", "anthropic", `data:{"type":"content_block_delta","index":0,` + "\n" + `data: "delta":{"type":"text_delta","text":"anthropic"}}` + "\n\n" + `data:{"type":"message_delta","delta":{"stop_reason":"end_turn"}}` + "\n\n" + `data:{"type":"message_stop"}` + "\n\n", "anthropic"},
	{"Gemini", "gemini", `data:{"candidates":[{"content":{"parts":[{"text":"gemini"}]},"finishReason":"STOP"}]}` + "\n\n", "gemini"},
	{"Mistral", "mistral", `data:{"choices":[{"delta":{"content":"mistral"},"finish_reason":"stop"}]}` + "\n\ndata:[DONE]\n\n", "mistral"},
	{"Cohere", "cohere", `data:{"type":"content-delta","delta":{"message":{"content":{"text":"cohere"}}}}` + "\n\n" + `data:{"type":"message-end","delta":{"finish_reason":"COMPLETE"}}` + "\n\n", "cohere"},
	{"Vercel Gateway", "vercel-gateway", `data:{"type":"text-delta","id":"text_1","delta":"gateway"}` + "\n\n" + `data:{"type":"finish","finishReason":{"unified":"stop"}}` + "\n\n", "gateway"},
}

func TestPinnedProvidersReadSSEWithoutSpace(t *testing.T) {
	for _, check := range pinnedSSEStreams {
		t.Run(check.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, ": proxy comment\n\ndata:\n\ndata:null\n\ndata:: keepalive\n\n")
				fmt.Fprint(w, check.stream)
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: check.protocol, ID: "mock", BaseURL: server.URL, APIKey: "key"}
			turn, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if err != nil || turn.Text != check.want {
				t.Fatalf("%s SSE without space: turn=%+v err=%v", check.protocol, turn, err)
			}
		})
	}
}

func TestPinnedProvidersRejectMalformedMultilineSSE(t *testing.T) {
	for _, check := range pinnedSSEStreams {
		for _, first := range []string{"null", ": keepalive"} {
			t.Run(check.protocol+"/"+first, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data:%s\ndata:{}\n\n", first)
					fmt.Fprint(w, check.stream)
				}))
				defer server.Close()
				model := modelcfg.Model{Provider: check.protocol, ID: "mock", BaseURL: server.URL, APIKey: "key"}
				if _, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil); err == nil {
					t.Fatalf("%s accepted malformed multiline SSE event after %q", check.protocol, first)
				}
			})
		}
	}
}

func TestNoSpaceSSEStillRejectsOversizedChatEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data:"+strings.Repeat("x", maxEventBytes)+"\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "openai-chat", ID: "mock", BaseURL: server.URL, APIKey: "key"}
	if _, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil); err == nil {
		t.Fatal("oversized no-space SSE event accepted")
	}
}

func TestSSEMultilineEventHonorsCumulativeLimit(t *testing.T) {
	input := "data:" + strings.Repeat("x", 80<<10) + "\n" + "data:" + strings.Repeat("x", 80<<10) + "\n\n"
	scanner := bufio.NewScanner(strings.NewReader(input))
	scanner.Buffer(make([]byte, 4096), maxEventBytes)
	if _, _, err := nextSSEData(scanner, maxEventBytes); err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("oversized multiline event accepted: %v", err)
	}
}
