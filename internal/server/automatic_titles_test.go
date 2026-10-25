package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"vista/internal/agentconfig"
	modelcfg "vista/internal/model"
	"vista/internal/store"
)

func TestAutomaticTitlesPartialSettings(t *testing.T) {
	app := &App{configDir: t.TempDir()}
	if err := os.Chmod(app.configDir, 0700); err != nil {
		t.Fatal(err)
	}
	request := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		app.agentSettings(w, httptest.NewRequest(http.MethodPut, "/api/agent-settings", strings.NewReader(body)))
		return w
	}
	for _, tc := range []struct {
		body    string
		enabled bool
		depth   int
	}{
		{`{"automaticTitles":false}`, false, 1},
		{`{"instructions":"Keep preferences separate.","delegationDepth":3}`, false, 3},
		{`{"automaticTitles":true}`, true, 3},
	} {
		if w := request(tc.body); w.Code != http.StatusNoContent {
			t.Fatalf("save: %d %s", w.Code, w.Body.String())
		}
		w := httptest.NewRecorder()
		app.agentSettings(w, httptest.NewRequest(http.MethodGet, "/api/agent-settings", nil))
		var got struct {
			AutomaticTitles bool `json:"automaticTitles"`
			DelegationDepth int  `json:"delegationDepth"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.AutomaticTitles != tc.enabled || got.DelegationDepth != tc.depth {
			t.Fatalf("partial edit lost another setting: %+v %v", got, err)
		}
	}
	for _, value := range []string{`null`, `0`, `"false"`, `{}`, `[]`} {
		if w := request(`{"instructions":"should not be written","automaticTitles":` + value + `}`); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid value accepted: %s %d", value, w.Code)
		}
	}
	if got, err := agentconfig.Read(app.configDir); err != nil || got != "Keep preferences separate." {
		t.Fatalf("invalid combined edit changed instructions: %q %v", got, err)
	}
}

func TestDisabledTitlesKeepDefaultWithoutModelRequests(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := &App{ctx: ctx, store: db, configDir: t.TempDir()}
	if err := os.Chmod(app.configDir, 0700); err != nil {
		t.Fatal(err)
	}
	setEnabled := func(enabled bool) {
		t.Helper()
		if err := agentconfig.WriteSettings(app.configDir, agentconfig.Settings{DelegationDepth: 1, AutomaticTitles: enabled}); err != nil {
			t.Fatal(err)
		}
	}
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Generated title\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	model := modelcfg.Model{Provider: "openai-chat", ID: "test", BaseURL: upstream.URL, APIKey: "test"}
	setEnabled(false)
	for _, tc := range []struct {
		id, title, prompt, want string
		manual                  bool
	}{
		{"text", "New session", "  Compare\n two phones\tfor gaming  ", "New session", false},
		{"unicode", "New session", strings.Repeat("图🙂", 25), "New session", false},
		{"named", "Existing title", "Do not rename", "Existing title", false},
		{"manual", "New session", "Do not rename", "New session", true},
		{"empty", "New session", "", "New session", false},
	} {
		if _, err := db.Create(ctx, tc.id, tc.title); err != nil {
			t.Fatal(err)
		}
		if tc.manual {
			if err := db.Rename(ctx, tc.id, tc.title); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := db.BeginRun(ctx, tc.id, tc.id+"-run", tc.id+"-user", tc.prompt); err != nil {
			t.Fatal(err)
		}
		app.generateSessionTitle(tc.id, tc.id+"-user", model)
		item, err := db.Get(ctx, tc.id)
		if err != nil || item.Title != tc.want || !utf8.ValidString(item.Title) {
			t.Fatalf("retained title: %+v %v", item, err)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("disabled titles invoked a model")
	}
	setEnabled(true)
	if _, err := db.FinishRun(ctx, "text", "text-run", "text-answer", "succeeded", "Answer"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.BeginRun(ctx, "text", "follow-up-run", "follow-up-user", "Another question"); err != nil {
		t.Fatal(err)
	}
	app.generateSessionTitle("text", "follow-up-user", model)
	if item, err := db.Get(ctx, "text"); err != nil || item.Title != "New session" || requests.Load() != 0 {
		t.Fatalf("reenabling titles renamed an existing conversation: %+v requests=%d %v", item, requests.Load(), err)
	}
	if _, err := db.Create(ctx, "enabled", "New session"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.BeginRun(ctx, "enabled", "enabled-run", "enabled-user", "Generate this title"); err != nil {
		t.Fatal(err)
	}
	app.generateSessionTitle("enabled", "enabled-user", model)
	item, err := db.Get(ctx, "enabled")
	if err != nil || item.Title != "Generated title" || requests.Load() != 1 {
		t.Fatalf("reenabled title: %+v requests=%d %v", item, requests.Load(), err)
	}
}
