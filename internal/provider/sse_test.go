package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	scanner := newSSELines(strings.NewReader(input))
	data, ok, err := nextSSEData(scanner)
	if err != nil || !ok || data != "null\n{}" {
		t.Fatalf("joined SSE event: data=%q ok=%v err=%v", data, ok, err)
	}
}

func TestSSEEmptyFirstDataFieldKeepsNewline(t *testing.T) {
	scanner := newSSELines(strings.NewReader("data:\ndata:{}\n\n"))
	data, ok, err := nextSSEData(scanner)
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

func TestNoSpaceSSERejectsMalformedLargeChatEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data:"+strings.Repeat("x", 128<<10)+"\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "openai-chat", ID: "mock", BaseURL: server.URL, APIKey: "key"}
	if _, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil); err == nil {
		t.Fatal("malformed no-space SSE event accepted")
	}
}

func TestSSEPreservesLargeMultilineEventAndFinalLine(t *testing.T) {
	part := strings.Repeat("x", 2<<20)
	input := "data:" + part + "\r\n" + "data:" + part
	scanner := newSSELines(strings.NewReader(input))
	data, ok, err := nextSSEData(scanner)
	if err != nil || !ok || data != part+"\n"+part {
		t.Fatalf("large multiline event: bytes=%d ok=%v err=%v", len(data), ok, err)
	}
	if _, ok, err := nextSSEData(scanner); ok || err != nil {
		t.Fatalf("after final line: ok=%v err=%v", ok, err)
	}
}

type failingSSEReader struct{ err error }

func (reader failingSSEReader) Read([]byte) (int, error) { return 0, reader.err }

func TestSSEPropagatesReadFailure(t *testing.T) {
	want := errors.New("upstream disconnected")
	reader := io.MultiReader(strings.NewReader("data: partial"), failingSSEReader{want})
	if _, ok, err := nextSSEData(newSSELines(reader)); ok || !errors.Is(err, want) {
		t.Fatalf("read error: ok=%v err=%v", ok, err)
	}
}

func TestPinnedProvidersPreserveLargeSSEEvent(t *testing.T) {
	answer := strings.Repeat("长", (2<<20)/3+1)
	for _, check := range pinnedSSEStreams {
		t.Run(check.name, func(t *testing.T) {
			stream := strings.Replace(check.stream, check.want, answer, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, stream)
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: check.protocol, ID: "mock", BaseURL: server.URL, APIKey: "key"}
			var streamed strings.Builder
			turn, err := ModelTurnStream(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil, func(delta string) error {
				streamed.WriteString(delta)
				return nil
			})
			if err != nil || turn.Text != answer || streamed.String() != answer {
				t.Fatalf("large SSE event: bytes=%d streamed=%d err=%v", len(turn.Text), streamed.Len(), err)
			}
		})
	}
}
