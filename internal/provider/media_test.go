package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
)

func testImage(t *testing.T) Image {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lN8AAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	return Image{MediaType: "image/png", Data: data}
}

func TestUserImageWireShapes(t *testing.T) {
	image := testImage(t)
	message := ChatMessage{Role: "user", Content: "What is this?", Images: []Image{image, image, image}}
	dataURL, err := imageDataURL(image)
	if err != nil {
		t.Fatal(err)
	}
	chat, err := chatMessagesWithReasoningDetails([]ChatMessage{message}, "openai")
	if err != nil {
		t.Fatal(err)
	}
	chatParts := chat[0]["content"].([]any)
	if len(chatParts) != 4 || chatParts[0].(map[string]any)["text"] != message.Content || chatParts[1].(map[string]any)["image_url"].(map[string]any)["url"] != dataURL {
		t.Fatalf("Chat image parts: %+v", chatParts)
	}
	_, anthropic, err := anthropicMessages([]ChatMessage{message})
	if err != nil {
		t.Fatal(err)
	}
	anthropicParts := anthropic[0]["content"].([]any)
	source := anthropicParts[1].(map[string]any)["source"].(map[string]any)
	if source["media_type"] != "image/png" || source["data"] != base64.StdEncoding.EncodeToString(image.Data) {
		t.Fatalf("Anthropic image source: %+v", source)
	}
	gemini, _, err := geminiContents([]ChatMessage{message})
	if err != nil {
		t.Fatal(err)
	}
	geminiParts := gemini[0]["parts"].([]any)
	inline := geminiParts[1].(map[string]any)["inlineData"].(map[string]any)
	if inline["mimeType"] != "image/png" || inline["data"] != source["data"] {
		t.Fatalf("Gemini image data: %+v", inline)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []struct {
				Content []map[string]any `json:"content"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Input) != 1 || len(body.Input[0].Content) != 4 || body.Input[0].Content[0]["type"] != "input_text" || body.Input[0].Content[1]["type"] != "input_image" || body.Input[0].Content[1]["image_url"] != dataURL {
			t.Errorf("Responses image parts: %+v", body.Input)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg\",\"delta\":\"seen\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer server.Close()
	turn, err := OpenAIResponsesTurn(context.Background(), modelcfg.Model{Provider: "openai-responses", ID: "test", BaseURL: server.URL, APIKey: "key"}, []ChatMessage{message}, nil)
	if err != nil || turn.Text != "seen" {
		t.Fatalf("Responses image turn: %+v, %v", turn, err)
	}
}

func TestUserImagesRejectInvalidOrUnboundedData(t *testing.T) {
	image := testImage(t)
	checks := []struct {
		name    string
		message ChatMessage
		want    string
	}{
		{"assistant image", ChatMessage{Role: "assistant", Images: []Image{image}}, "user message"},
		{"wrong media type", ChatMessage{Role: "user", Images: []Image{{MediaType: "image/jpeg", Data: image.Data}}}, "does not match"},
		{"oversized image", ChatMessage{Role: "user", Images: []Image{{MediaType: "image/png", Data: bytes.Repeat([]byte("x"), maxImageBytes+1)}}}, "50 MiB"},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if _, err := chatMessagesWithReasoningDetails([]ChatMessage{check.message}, "openai"); err == nil || !strings.Contains(err.Error(), check.want) {
				t.Fatalf("Chat accepted invalid image: %v", err)
			}
			if _, _, err := anthropicMessages([]ChatMessage{check.message}); err == nil || !strings.Contains(err.Error(), check.want) {
				t.Fatalf("Anthropic accepted invalid image: %v", err)
			}
			if _, _, err := geminiContents([]ChatMessage{check.message}); err == nil || !strings.Contains(err.Error(), check.want) {
				t.Fatalf("Gemini accepted invalid image: %v", err)
			}
		})
	}
	_, err := ModelTurn(context.Background(), modelcfg.Model{Provider: "bedrock-mantle-chat", ID: "test"}, []ChatMessage{{Role: "user", Images: []Image{image}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "image input is not supported") {
		t.Fatalf("unsupported protocol silently dropped image: %v", err)
	}
}

func TestImageRequestHasNoAggregateByteLimit(t *testing.T) {
	image := testImage(t)
	image.Data = append(append([]byte(nil), image.Data...), bytes.Repeat([]byte{0}, (1<<20)-len(image.Data))...)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.ContentLength <= 4<<20 {
			t.Errorf("fixture did not exceed the old request cap: %d", r.ContentLength)
		}
		var request struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) != 1 {
			t.Errorf("large media payload: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	messages := []ChatMessage{{Role: "user", Content: "Describe all images", Images: []Image{image, image, image, image}}}
	turn, err := OpenAIChatTurn(context.Background(), modelcfg.Model{Provider: "openai-chat", ID: "test", APIKey: "key", BaseURL: server.URL}, messages, nil)
	if err != nil || turn.Text != "ok" || requests != 1 {
		t.Fatalf("large image request: requests=%d turn=%+v err=%v", requests, turn, err)
	}
}

func TestToolImagesFollowCompleteParallelResultsAcrossProtocols(t *testing.T) {
	image := testImage(t)
	first, second := ToolCall{ID: "first", Type: "function"}, ToolCall{ID: "second", Type: "function"}
	first.Function.Name, first.Function.Arguments = "read_resource", `{}`
	second.Function.Name, second.Function.Arguments = "read_resource", `{}`
	messages := []ChatMessage{
		{Role: "user", Content: "Inspect both originals"},
		{Role: "assistant", ToolCalls: []ToolCall{first, second}},
		{Role: "tool", ToolCallID: first.ID, Content: "first result", Images: []Image{image}},
		{Role: "tool", ToolCallID: second.ID, Content: "second result", Images: []Image{image}},
	}
	projected := projectToolImages(messages)
	var roles []string
	for _, message := range projected {
		roles = append(roles, message.Role)
	}
	if !reflect.DeepEqual(roles, []string{"user", "assistant", "tool", "tool", "user"}) || len(projected[4].Images) != 2 || len(messages[2].Images) != 1 || len(messages[3].Images) != 1 {
		t.Fatalf("tool result ordering or canonical history changed: %+v", roles)
	}
	if _, err := chatMessagesWithReasoningDetails(projected, "openai"); err != nil {
		t.Fatalf("Chat rejected resource image: %v", err)
	}
	_, anthropic, err := anthropicMessages(projected)
	if err != nil || len(anthropic) != 5 {
		t.Fatalf("Anthropic rejected resource image: %+v %v", anthropic, err)
	}
	gemini, _, err := geminiContents(projected)
	if err != nil || len(gemini) != 4 {
		t.Fatalf("Gemini rejected resource image: %+v %v", gemini, err)
	}
	if parts := gemini[2]["parts"].([]any); len(parts) != 2 {
		t.Fatal("Gemini parallel responses were split by a runtime image")
	}
	encoded := base64.StdEncoding.EncodeToString(image.Data)
	for _, wire := range []any{anthropic, gemini} {
		data, _ := json.Marshal(wire)
		if bytes.Count(data, []byte(encoded)) != 2 {
			t.Fatal("protocol discarded an original image")
		}
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Input) != 6 || body.Input[3]["type"] != "function_call_output" || body.Input[3]["call_id"] != "first" || body.Input[4]["call_id"] != "second" || body.Input[5]["role"] != "user" {
			t.Errorf("Responses result order changed: %+v", body.Input)
		}
		wire, _ := json.Marshal(body.Input)
		if bytes.Count(wire, []byte(encoded)) != 2 {
			t.Error("Responses discarded an original image")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"seen\"}]}]}}\n\n")
	}))
	defer upstream.Close()
	if turn, err := ModelTurn(context.Background(), modelcfg.Model{Provider: "openai-responses", ID: "fixture", BaseURL: upstream.URL, APIKey: "fixture"}, messages, nil); err != nil || turn.Text != "seen" {
		t.Fatalf("Responses resource turn: %+v %v", turn, err)
	}
}
