package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vista/internal/schedule"
	"vista/internal/store"
)

func TestScheduledStateIsScopedAndDoesNotBroadcastMessage(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := &App{store: db, ctx: ctx}
	previous := app.updates.snapshot()
	const prompt = "A private message used only for executing this task."
	req := httptest.NewRequest(http.MethodPost, "/api/schedules", strings.NewReader(`{"name":"Test","prompt":"`+prompt+`","rule":{"kind":"interval","timezone":"UTC","minutes":60}}`))
	w := httptest.NewRecorder()
	app.schedulesHTTP(w, req)
	if w.Code != http.StatusCreated || strings.Contains(w.Body.String(), prompt) {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	point, err := app.stateUpdateScoped(ctx, "", previous, map[string]bool{}, nil)
	if err != nil || point.Schedules != nil || point.Revisions["schedules"] <= previous.Revisions["schedules"] {
		t.Fatalf("unsubscribed state %+v %v", point, err)
	}
	point, err = app.stateUpdateScoped(ctx, "", previous, map[string]bool{"schedules": true}, nil)
	if err != nil || point.Schedules == nil || len(point.Schedules.Items) != 1 || point.Schedules.Items[0].Prompt != "" {
		t.Fatalf("subscribed state %+v %v", point, err)
	}
	w = httptest.NewRecorder()
	app.schedulesHTTP(w, httptest.NewRequest(http.MethodGet, "/api/schedules", nil))
	var snapshot scheduleSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil || snapshot.Version != point.Schedules.Version || strings.Contains(w.Body.String(), prompt) {
		t.Fatalf("HTTP recovery %d %s %v", w.Code, w.Body.String(), err)
	}
	w = httptest.NewRecorder()
	app.schedulesHTTP(w, httptest.NewRequest(http.MethodGet, "/api/schedules/"+point.Schedules.Items[0].ID, nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), prompt) {
		t.Fatalf("on-demand editor %d %s", w.Code, w.Body.String())
	}
}

func TestScheduledMissingPinnedModelNeverCreatesSession(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := &App{store: db, ctx: ctx, started: time.Now().Add(-time.Minute)}
	now := time.Now().UnixMilli()
	item, err := db.SaveSchedule(ctx, store.Schedule{Name: "Pinned", Prompt: "Hello", ModelKey: "gone", Rule: schedule.Rule{Kind: "interval", Minutes: 60, Timezone: "UTC"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueueScheduleNow(ctx, item.ID, "run", now); err != nil {
		t.Fatal(err)
	}
	if _, err := app.stepSchedules(); err != nil {
		t.Fatal(err)
	}
	list, err := db.ScheduleList(ctx)
	if err != nil || len(list) != 1 || list[0].Enabled || list[0].Blocked != "model" || list[0].Latest.State != "failed" {
		t.Fatalf("unexpected plan state %+v %v", list, err)
	}
	if sessions, err := db.List(ctx); err != nil || len(sessions) != 0 {
		t.Fatalf("missing model started a session %+v %v", sessions, err)
	}
}
