package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vista/internal/store"
)

func TestSessionScopeHTTPAndStatePayloads(t *testing.T) {
	consumer, _ := newResultConsumerTest(t)
	app := &App{store: consumer.store}
	ctx := context.Background()
	app.updates.notify("sessions")
	before, err := app.stateUpdate(ctx, "localhost", stateCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	baseline, _ := json.Marshal(before.Sessions)
	for i := range 150 {
		id := fmt.Sprintf("archive-%03d", i)
		if _, err := app.store.Create(ctx, id, "Archived-only "+strings.Repeat("long title ", 15)); err != nil {
			t.Fatal(err)
		}
		if err := app.store.SetOpen(ctx, id, false); err != nil {
			t.Fatal(err)
		}
	}
	app.updates.notify("sessions", "status")
	after, err := app.stateUpdate(ctx, "localhost", stateCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(after.Sessions)
	if string(payload) != string(baseline) {
		t.Fatalf("archive growth changed workspace payload: %d -> %d bytes", len(baseline), len(payload))
	}
	frame, _ := json.Marshal(after)
	if strings.Contains(string(frame), "Archived-only") || strings.Contains(string(frame), "archive-000") || len(frame) > 8000 {
		t.Fatalf("archives leaked into global SSE: %d bytes", len(frame))
	}
	counts := after.Status["sessions"].(map[string]int)
	if counts["closed"] != 150 || counts["total"] != 151 {
		t.Fatalf("missing aggregate counts: %+v", counts)
	}
	for _, tc := range []struct {
		query string
		count int
		open  bool
	}{
		{"?open=true", 1, true}, {"?open=false", 150, false}, {"", 151, false},
	} {
		w := httptest.NewRecorder()
		app.sessions(w, httptest.NewRequest(http.MethodGet, "/api/sessions"+tc.query, nil))
		var items []store.Session
		if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil || w.Code != 200 || len(items) != tc.count {
			t.Fatalf("scoped list %s: %d entries status=%d err=%v", tc.query, len(items), w.Code, err)
		}
		if tc.query != "" {
			for _, item := range items {
				if item.Open != tc.open {
					t.Fatalf("wrong scope: %+v", item)
				}
			}
		}
	}
	for _, query := range []string{"?open=invalid", "?open=", "?open=true&open=false"} {
		w := httptest.NewRecorder()
		app.sessions(w, httptest.NewRequest(http.MethodGet, "/api/sessions"+query, nil))
		if w.Code != 400 {
			t.Fatalf("invalid scope accepted: %s %d", query, w.Code)
		}
	}
}

func TestSingleSessionCheckpointDistinguishesClosedFromDeleted(t *testing.T) {
	consumer, _ := newResultConsumerTest(t)
	app := &App{store: consumer.store}
	ctx := context.Background()
	const id = "00000000-0000-4000-8000-000000000010"
	if _, err := app.store.Create(ctx, id, "Closed but readable"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := app.store.BeginRun(ctx, id, "closed-run", "closed-user", "Keep history"); err != nil {
		t.Fatal(err)
	}
	if err := app.store.SetOpen(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	read := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		app.session(w, httptest.NewRequest(http.MethodGet, "/api/sessions/"+id+"/checkpoint", nil))
		return w
	}
	w := read()
	var point store.SyncCheckpoint
	if err := json.Unmarshal(w.Body.Bytes(), &point); err != nil || w.Code != 200 || point.Session.ID != id || point.Session.Open || point.ThroughSequence == 0 {
		t.Fatalf("closed checkpoint: %d %+v %v", w.Code, point, err)
	}
	if strings.Contains(w.Body.String(), "Keep history") {
		t.Fatal("metadata read included conversation history")
	}
	if err := app.store.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if w := read(); w.Code != 404 {
		t.Fatalf("deleted checkpoint: %d", w.Code)
	}
}
