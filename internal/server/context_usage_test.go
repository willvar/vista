package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"vista/internal/agent"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestContextUsageRestoresLiveEstimateAndRebuildsIdleContext(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	dir := filepath.Join(root, "vista", "state")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	a := &App{store: db, models: modelCatalog{Default: "fixture", Models: []configuredModel{{Key: "fixture", ID: "fixture", Provider: "openai-chat"}}}}
	if _, err := db.Create(ctx, "session", "context"); err != nil {
		t.Fatal(err)
	}
	read := func() workerproto.ContextUsage {
		t.Helper()
		w := httptest.NewRecorder()
		a.contextUsage(w, httptest.NewRequest("GET", "/api/sessions/session/context-usage", nil), "session")
		var usage workerproto.ContextUsage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &usage) != nil || usage.Tokens <= 0 {
			t.Fatalf("context estimate: %d %s", w.Code, w.Body.String())
		}
		return usage
	}
	empty := read()
	if err := db.SetReplyLanguage(ctx, "session", "en-GB"); err != nil {
		t.Fatal(err)
	}
	model, _, err := a.modelsSnapshot().Resolve("fixture")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := agent.EstimateSessionContext(ctx, workerproto.Command{SessionID: "session", Model: model, ReplyLanguage: "en-GB"})
	if err != nil {
		t.Fatal(err)
	}
	localized := read()
	if localized.Tokens != expected || localized.Tokens == empty.Tokens {
		t.Fatalf("idle estimate lost the saved language: empty=%d selected=%d expected=%d", empty.Tokens, localized.Tokens, expected)
	}
	empty = localized
	if _, _, err := db.BeginRun(ctx, "session", "run", "u", "Retain this user task"); err != nil {
		t.Fatal(err)
	}
	c := &workerResultConsumer{ctx: ctx, store: db, sessionID: "session", notify: func() {}, send: func(workerproto.Command) error { return nil }}
	result := workerproto.Result{RunID: "run", Kind: "context_usage", ContextUsage: &workerproto.ContextUsage{Tokens: 50000}}
	if err := c.consume(result); err != nil {
		t.Fatal(err)
	}
	live := read()
	if live.Tokens != 50000 || live.RunID != "run" {
		t.Fatalf("lost live snapshot: %+v", live)
	}
	result.Depth = 1
	if err := c.consume(result); err == nil {
		t.Fatal("child replaced main context")
	}
	// Hundreds of unrelated events must not evict the context snapshot.
	for i := 0; i < 160; i++ {
		if _, err := db.AppendRunEvent(ctx, "session", "run", "source", map[string]string{"runId": "run", "content": "unrelated"}); err != nil {
			t.Fatal(err)
		}
	}
	if current := read(); current != live {
		t.Fatalf("event window changed context: %+v", current)
	}
	if _, err := db.FinishRun(ctx, "session", "run", "a", "succeeded", "Retain this final answer"); err != nil {
		t.Fatal(err)
	}
	idle := read()
	if idle.Tokens <= empty.Tokens || idle.Tokens >= live.Tokens || idle.Sequence <= live.Sequence {
		t.Fatalf("idle estimate retained discarded execution history: empty=%+v live=%+v idle=%+v", empty, live, idle)
	}
	result.Depth = 0
	if err := c.consume(result); err != nil {
		t.Fatal(err)
	}
	if reloaded := read(); reloaded != idle {
		t.Fatalf("late result changed the idle context: %+v", reloaded)
	}
}
