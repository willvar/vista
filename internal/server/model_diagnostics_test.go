package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"vista/internal/modeltrace"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestModelDiagnosticsAPIIndependentReadAndRunOwnership(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	session, other, run, id := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, s := range []string{session, other} {
		if _, err := st.Create(ctx, s, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := st.BeginRun(ctx, session, run, "input", "hello"); err != nil {
		t.Fatal(err)
	}
	record := modeltrace.Record{Scope: modeltrace.Scope{SessionID: session, RunID: run, Turn: 1}, ID: id, StartedAt: time.Now(), Protocol: "openai-chat", Model: "fixture", Error: "parser failed", Exchanges: []*modeltrace.Exchange{{Status: 200, Response: modeltrace.Body{Content: "data: failed\n\n", Encoding: "utf-8", Bytes: 14}}}}
	if err := modeltrace.Save(dir, record); err != nil {
		t.Fatal(err)
	}
	app := &App{store: st, dir: dir}
	base := "/api/sessions/" + session + "/runs/" + run + "/diagnostics"
	for _, check := range []struct {
		path string
		code int
	}{
		{base, 200}, {base + "/" + id, 200}, {base + "/" + uuid.NewString(), 404},
		{"/api/sessions/" + other + "/runs/" + run + "/diagnostics", 404},
		{"/api/sessions/" + session + "/runs/" + uuid.NewString() + "/diagnostics", 404},
		{base + "/unsafe", 404},
	} {
		w := httptest.NewRecorder()
		// No stream subscription or debug cookie is needed to inspect a saved record.
		app.session(w, httptest.NewRequest(http.MethodGet, check.path, nil))
		if w.Code != check.code {
			t.Fatalf("%s: %d %s", check.path, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("diagnostics must not be cached")
		}
		if check.path == base+"/"+id && !strings.Contains(w.Body.String(), "data: failed") {
			t.Fatal("missing original response")
		}
		if check.path == base {
			var list []modeltrace.Summary
			if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) != 1 || strings.Contains(w.Body.String(), "data: failed") {
				t.Fatalf("index eagerly included response body: %s %v", w.Body.String(), err)
			}
		}
	}
}

func TestDebugPreferenceTravelsWithQueuedInput(t *testing.T) {
	app, session := inputQueueFixture(t)
	input, _, err := app.store.QueueInputWithDiagnostics(app.ctx, session, uuid.NewString(), "now", "capture this", nil, true)
	if err != nil || !input.Debug {
		t.Fatalf("queue preference: %+v %v", input, err)
	}
	command, err := app.pullInterjection(app.ctx, session, "run")
	if err != nil || command == nil || !command.Debug {
		t.Fatalf("interjection preference: %+v %v", command, err)
	}
	input, _, err = app.store.QueueInputWithDiagnostics(app.ctx, session, uuid.NewString(), "later", "later debug request", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := app.store.NextQueuedInput(app.ctx, session)
	if err != nil || pending == nil || pending.ID != input.ID || !pending.Debug {
		t.Fatalf("queued run preference: %+v %v", pending, err)
	}
	inputs, err := app.store.RecentInputs(app.ctx, session)
	if err != nil || len(inputs) != 2 || !inputs[0].Debug || !inputs[1].Debug {
		t.Fatalf("durable preference: %+v %v", inputs, err)
	}
}

func TestRequestDebugCookieDoesNotEnableOtherClients(t *testing.T) {
	for _, value := range []string{"", "false", "true"} {
		r := httptest.NewRequest(http.MethodPost, "/api/sessions", nil)
		if value != "" {
			r.AddCookie(&http.Cookie{Name: "vista-debug-mode", Value: value})
		}
		if requestDebugMode(r) != (value == "true") {
			t.Fatalf("debug cookie %q", value)
		}
	}
	var command workerproto.Command
	if err := json.Unmarshal([]byte(`{"debug":true,"runId":"run"}`), &command); err != nil || !command.Debug {
		t.Fatalf("worker debug transport: %+v %v", command, err)
	}
}
