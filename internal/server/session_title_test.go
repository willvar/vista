package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/replylanguage"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestMakeSessionTitleUsesModelWithoutTools(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model           string `json:"model"`
			ReasoningEffort string `json:"reasoning_effort"`
			Messages        []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools []any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("x-opencode-session") == "" || body.Model != "deepseek-v4-flash" || body.ReasoningEffort != "" || len(body.Messages) != 2 || len(body.Tools) != 0 || !strings.Contains(body.Messages[1].Content, "排查构建错误") {
			t.Errorf("unexpected title request: %s %+v", r.URL.Path, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"先判断主题\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"《构建依赖冲突》\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	model := modelcfg.Model{Provider: "openai-chat", ProviderID: "opencode-go", ID: "deepseek-v4-flash", BaseURL: upstream.URL + "/v1", APIKey: "test"}
	title, err := makeSessionTitle(context.Background(), model, "排查构建错误", nil)
	if err != nil || title != "构建依赖冲突" {
		t.Fatalf("model generated title: %q %v", title, err)
	}
}

func TestMakeSessionTitleDisablesSupportedGPT5Reasoning(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Reasoning map[string]any `json:"reasoning"`
			Tools     []any          `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Reasoning) != 0 || len(body.Tools) != 0 {
			t.Errorf("title reasoning/tools: %+v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"item_id\":\"rs_1\",\"delta\":\"Choose a short title\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"Build error\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer upstream.Close()
	model := modelcfg.Model{Provider: "openai-responses", ProviderID: "openai", ID: "gpt-5.4", BaseURL: upstream.URL + "/v1", APIKey: "key", ReasoningEffort: "high", ReasoningSummary: "detailed"}
	if title, err := makeSessionTitle(context.Background(), model, "Investigate build error", nil); err != nil || title != "Build error" {
		t.Fatalf("title request: %q %v", title, err)
	}
}

func TestMakeSessionTitleOmitsExplicitEffort(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Reasoning map[string]any `json:"reasoning"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Reasoning) != 0 {
			t.Fatalf("title carried explicit effort: %+v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"Private model title\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer upstream.Close()
	model := modelcfg.Model{Provider: "openai-responses", ID: "private-reasoning-model", BaseURL: upstream.URL + "/v1", APIKey: "key", ReasoningEffort: "high"}
	if title, err := makeSessionTitle(context.Background(), model, "Investigate private model", nil); err != nil || title != "Private model title" {
		t.Fatalf("unknown-model title request: %q %v", title, err)
	}
}

func TestMakeSessionTitleUnknownModelCanBeRejectedWhenEffortIsRequired(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Reasoning map[string]any `json:"reasoning"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Reasoning) != 0 {
			t.Fatal("fixture should reject only the missing effort case")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"reasoning effort is required"}}`))
	}))
	defer upstream.Close()
	model := modelcfg.Model{Provider: "openai-responses", ID: "private-reasoning-model", BaseURL: upstream.URL + "/v1", APIKey: "key"}
	if _, err := makeSessionTitle(context.Background(), model, "Investigate private model", nil); err == nil {
		t.Fatal("unknown model unexpectedly generated a title without an effort")
	}
}

func TestMakeSessionTitleIncludesImageOnlyFirstMessage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools []any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		var parts []map[string]any
		if len(body.Messages) == 2 {
			if err := json.Unmarshal(body.Messages[1].Content, &parts); err != nil {
				t.Error(err)
			}
		}
		if len(body.Messages) != 2 || len(parts) != 2 || parts[1]["type"] != "image_url" || len(body.Tools) != 0 {
			t.Errorf("image title request: %+v", body)
		}
		var system string
		if len(body.Messages) == 0 || json.Unmarshal(body.Messages[0].Content, &system) != nil || !strings.Contains(system, "Default to replies in Traditional Chinese (Hong Kong)") {
			t.Errorf("image-only title lost reply language: %s", system)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"Identify the picture\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"照片中的猫\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	model := modelcfg.Model{Provider: "openai-chat", ID: "vision", BaseURL: upstream.URL + "/v1", APIKey: "key"}
	title, err := makeSessionTitle(replylanguage.WithDefault(context.Background(), "zh-HK"), model, "", []provider.Image{{MediaType: "image/png", Data: testPNG(t)}})
	if err != nil || title != "照片中的猫" {
		t.Fatalf("image title: %q %v", title, err)
	}
}

func TestSessionTitleFailureLogKeepsProviderDetailsPrivate(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	model := modelcfg.Model{ProviderID: "opencode-go", ID: "deepseek-v4-flash", APIKey: "private-key"}
	for _, item := range []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "reason=timeout status=0"},
		{context.Canceled, "reason=cancelled status=0"},
		{errSessionTitleTools, "reason=unexpected_tools status=0"},
		{errSessionTitleEmpty, "reason=invalid_title status=0"},
		{&provider.ModelHTTPError{Provider: "private-upstream", StatusCode: 429, Code: "private-code"}, "reason=upstream_http status=429"},
		{errors.New("private-prompt private-key"), "reason=failed status=0"},
	} {
		output.Reset()
		logSessionTitleFailure("session", model, "model", item.err)
		got := output.String()
		if !strings.Contains(got, item.want) || !strings.Contains(got, `session="session" provider="opencode-go" model="deepseek-v4-flash" stage=model`) || strings.Contains(got, "private-") {
			t.Fatalf("unexpected title failure log: %q", got)
		}
	}
}

func TestCleanSessionTitleRejectsLongOrEmptyOutput(t *testing.T) {
	for _, value := range []string{"", "New session", strings.Repeat("题", 61)} {
		if title := cleanSessionTitle(value); title != "" {
			t.Fatalf("invalid title %q accepted as %q", value, title)
		}
	}
	if title := cleanSessionTitle("\"Build dependency conflict.\"\nExtra text"); title != "Build dependency conflict" {
		t.Fatalf("title cleanup: %q", title)
	}
}

func TestGenerateSessionTitleBeforeReplyUsesSelectedModel(t *testing.T) {
	selectedRequests := 0
	selectedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selectedRequests++
		var body struct {
			Model     string         `json:"model"`
			Tools     []any          `json:"tools"`
			Reasoning map[string]any `json:"reasoning"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/v1/responses" || body.Model != "gpt-6-luna" || len(body.Tools) != 0 || len(body.Reasoning) != 0 {
			t.Errorf("title request: path=%s body=%+v", r.URL.Path, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"Build dependency conflict\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer selectedServer.Close()
	defaultServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("title request used global default model")
	}))
	defer defaultServer.Close()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const sessionID = "00000000-0000-4000-8000-000000000096"
	if _, err := db.Create(ctx, sessionID, "New session"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.BeginRun(ctx, sessionID, "run", "user", "Investigate the build failure", "selected"); err != nil {
		t.Fatal(err)
	}
	app := &App{ctx: ctx, store: db, models: modelCatalog{Default: "default", Models: []configuredModel{
		{Key: "default", Provider: "openai-chat", ID: "default-model", BaseURL: defaultServer.URL + "/v1", APIKey: "default"},
		{Key: "selected", Name: "Herdex", Provider: "openai-responses", ProviderID: "openai", ID: "gpt-6-luna", BaseURL: selectedServer.URL + "/v1", APIKey: "selected", ReasoningEffort: "high"},
	}}}
	selectedModel, _, err := app.modelsSnapshot().ResolveForRun(ctx, "selected")
	if err != nil {
		t.Fatal(err)
	}
	app.generateSessionTitle(sessionID, "user", selectedModel)
	item, err := db.Get(ctx, sessionID)
	if err != nil || item.Title != "Build dependency conflict" || selectedRequests != 1 {
		t.Fatalf("selected model title generation: %+v requests=%d err=%v", item, selectedRequests, err)
	}
	if runID, err := db.RunningRunID(ctx, sessionID); err != nil || runID != "run" {
		t.Fatalf("title waited for main reply: run=%q err=%v", runID, err)
	}
}

func TestSubmitFirstMessageStartsTitleRequestBeforeReply(t *testing.T) {
	for _, tc := range []struct {
		code, language string
	}{
		{"", ""},
		{"zh-CN", "Simplified Chinese"},
		{"zh-TW", "Traditional Chinese (Taiwan)"},
		{"zh-HK", "Traditional Chinese (Hong Kong)"},
		{"en-GB", "British English"},
		{"pt-PT", "European Portuguese"},
	} {
		t.Run("language="+tc.code, func(t *testing.T) {
			requested := make(chan struct{}, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages []struct{ Role, Content string }
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) != 2 {
					t.Errorf("title request: %+v %v", body, err)
				} else {
					system := body.Messages[0].Content
					if tc.code == "" {
						if !strings.Contains(system, "Use the same language as the user's message; follow the user's explicit language requests.") || strings.Contains(system, "Default to replies in") {
							t.Errorf("missing language fallback: %s", system)
						}
					} else if !strings.Contains(system, "Default to replies in "+tc.language+"; follow the user's explicit language requests.") || strings.Count(system, "Default to replies in") != 1 || strings.Contains(system, "same language") {
						t.Errorf("title lost the selected language or kept a conflicting rule: %s", system)
					}
				}
				requested <- struct{}{}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Build error\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer upstream.Close()
			ctx := context.Background()
			db, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			const sessionID = "00000000-0000-4000-8000-000000000091"
			if _, err := db.Create(ctx, sessionID, "New session"); err != nil {
				t.Fatal(err)
			}
			sink := &imageCommandSink{}
			app := &App{ctx: ctx, store: db, models: modelCatalog{Default: "selected", Models: []configuredModel{{Key: "selected", Provider: "openai-chat", ID: "model", BaseURL: upstream.URL + "/v1", APIKey: "key"}}}, workers: map[string]*worker{sessionID: {input: sink}}}
			w := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/messages", bytes.NewBufferString(`{"content":"Investigate build error","modelKey":"selected"}`))
			request.Header.Set("X-Vista-Language", tc.code)
			app.submitMessage(w, request, sessionID)
			if w.Code != http.StatusAccepted {
				t.Fatalf("submit status %d: %s", w.Code, w.Body.String())
			}
			var command workerproto.Command
			if err := json.NewDecoder(sink).Decode(&command); err != nil || command.ReplyLanguage != tc.code {
				t.Fatalf("main request lost reply language: %+v %v", command, err)
			}
			select {
			case <-requested:
			case <-time.After(2 * time.Second):
				t.Fatal("first message did not start title request")
			}
			if runID, err := db.RunningRunID(ctx, sessionID); err != nil || runID == "" {
				t.Fatalf("title request waited for reply: run=%q err=%v", runID, err)
			}
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				item, err := db.Get(ctx, sessionID)
				if err != nil {
					t.Fatal(err)
				}
				if item.Title == "Build error" {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("title was not saved while the main run remained active")
		})
	}
}

func TestGenerateSessionTitleIgnoresLaterMessages(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const sessionID = "00000000-0000-4000-8000-000000000095"
	if _, err := db.Create(ctx, sessionID, "New session"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.BeginRun(ctx, sessionID, "run", "user", "Find the error"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRun(ctx, sessionID, "run", "answer", "failed", "Unavailable"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.BeginRun(ctx, sessionID, "run-2", "user-2", "A second topic"); err != nil {
		t.Fatal(err)
	}
	app := &App{ctx: ctx, store: db}
	app.generateSessionTitle(sessionID, "user-2", modelcfg.Model{Provider: "openai-chat", ID: "model", BaseURL: "https://example.invalid/v1", APIKey: "key"})
	item, err := db.Get(ctx, sessionID)
	if err != nil || item.Title != "New session" {
		t.Fatalf("later message changed title: %+v %v", item, err)
	}
}

func TestGenerateSessionTitleDoesNotOverwriteManualRename(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const sessionID = "00000000-0000-4000-8000-000000000093"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := db.Rename(ctx, sessionID, "Manual title"); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Automatic title\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	if _, err := db.Create(ctx, sessionID, "New session"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.BeginRun(ctx, sessionID, "run", "user", "Investigate a build error"); err != nil {
		t.Fatal(err)
	}
	app := &App{ctx: ctx, store: db}
	app.generateSessionTitle(sessionID, "user", modelcfg.Model{Provider: "openai-chat", ID: "model", BaseURL: upstream.URL + "/v1", APIKey: "key"})
	item, err := db.Get(ctx, sessionID)
	if err != nil || item.Title != "Manual title" {
		t.Fatalf("manual rename overwritten: %+v %v", item, err)
	}
}

func TestGenerateSessionTitleLeavesPlaceholderOnProviderFailure(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const sessionID = "00000000-0000-4000-8000-000000000092"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	if _, err := db.Create(ctx, sessionID, "New session"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.BeginRun(ctx, sessionID, "run", "user", "Investigate a build error"); err != nil {
		t.Fatal(err)
	}
	app := &App{ctx: ctx, store: db}
	app.generateSessionTitle(sessionID, "user", modelcfg.Model{Provider: "openai-chat", ID: "model", BaseURL: upstream.URL + "/v1", APIKey: "key"})
	item, err := db.Get(ctx, sessionID)
	if err != nil || item.Title != "New session" {
		t.Fatalf("failed title request changed title: %+v %v", item, err)
	}
}
