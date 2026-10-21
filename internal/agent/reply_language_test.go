package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/workerproto"
)

func TestReplyLanguageRebuildsSystemPromptWithoutAccumulatingInstructions(t *testing.T) {
	messages := []provider.ChatMessage{{Role: "system", Content: "base"}, {Role: "user", Content: "original message"}}
	prepared := preparedContext{systemBase: "base", replyLanguage: "zh-TW"}
	for _, code := range []string{"en", "en-GB", "zh-CN", "zh-TW", "zh-HK", "zh-MO", "zh-SG", "zh-MY", "es", "es-MX", "de", "de-CH", "ja", "ko", "fr", "pt-BR", "pt-PT", "ru"} {
		prepared.replyLanguage = code
		for range 2 {
			prepared.applyModel(messages, modelcfg.Model{})
			prompt := messages[0].Content
			if workerproto.ReplyLanguageName(code) == "" || strings.Count(prompt, "Default to replies in") != 1 || !strings.Contains(prompt, workerproto.ReplyLanguageName(code)) || !strings.Contains(prompt, "follow the user's explicit language requests") {
				t.Fatalf("bad system prompt: %s", prompt)
			}
			if messages[1].Content != "original message" {
				t.Fatal("modified user content")
			}
		}
	}
	prepared.replyLanguage = "not a language"
	prepared.applyModel(messages, modelcfg.Model{})
	if messages[0].Content != "base" {
		t.Fatal("unvalidated language entered prompt")
	}
}

func TestInterjectionChangesDefaultReplyLanguageOnNextRequest(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) == 0 {
			t.Errorf("request: %v", err)
			w.WriteHeader(400)
			return
		}
		want := "Traditional Chinese (Hong Kong)"
		if calls.Add(1) == 2 {
			want = "Japanese"
		}
		if prompt := body.Messages[0].Content; strings.Count(prompt, "Default to replies in") != 1 || !strings.Contains(prompt, "Default to replies in "+want) {
			t.Errorf("language missing or stale: %s", prompt)
		}
		continuationAnswer(w, "done")
	}))
	defer upstream.Close()
	command := workerproto.Command{RunID: "language-run", Prompt: "Start", ReplyLanguage: "zh-HK", Model: modelcfg.Model{Provider: "openai-chat", ID: "test", BaseURL: upstream.URL, APIKey: "test"}}
	inputs := make(chan workerproto.Command, 1)
	answer, err := RunWithInputs(context.Background(), command, func(event workerproto.Result) error {
		if event.Kind == "model_result" && event.Turn == 1 {
			inputs <- workerproto.Command{RunID: command.RunID, InputID: "next", Prompt: "Continue", ReplyLanguage: "ja"}
		}
		return nil
	}, inputs)
	if err != nil || answer != "done" || calls.Load() != 2 {
		t.Fatalf("answer=%q calls=%d error=%v", answer, calls.Load(), err)
	}
}
