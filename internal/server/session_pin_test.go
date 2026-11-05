package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vista/internal/store"
)

func TestSessionPinHTTPPublishesCommittedSummaryWithoutUnrelatedState(t *testing.T) {
	consumer, _ := newResultConsumerTest(t)
	app := &App{store: consumer.store}
	const id = "00000000-0000-4000-8000-000000000020"
	if _, err := app.store.Create(context.Background(), id, "Pinned conversation"); err != nil {
		t.Fatal(err)
	}
	handler := app.stateMutations(http.HandlerFunc(app.session))
	patch := func(body string, wantCode int) store.Session {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/api/sessions/"+id, strings.NewReader(body)))
		if w.Code != wantCode {
			t.Fatalf("PATCH %s: %d %s", body, w.Code, w.Body.String())
		}
		var item store.Session
		if wantCode == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
				t.Fatal(err)
			}
		}
		return item
	}
	for _, body := range []string{`{}`, `{"pinned":null}`, `{"pinned":"true"}`, `{"pinned":true,"title":"both"}`, `{"pinned":true,"unknown":true}`, `{"pinned":true} {}`} {
		patch(body, 400)
	}
	before := app.updates.snapshot()
	if before.Revisions["sessions"] != 0 {
		t.Fatal("invalid updates published a session change")
	}
	if item := patch(`{"pinned":true}`, 200); !item.Pinned || item.Title != "Pinned conversation" {
		t.Fatalf("pin response: %+v", item)
	}
	point, err := app.stateUpdate(context.Background(), "localhost", before)
	if err != nil || point.Sessions == nil || !point.Sessions.Items[0].Pinned || point.Sessions.Items[0].ID != id || point.Status != nil || point.Models != nil {
		t.Fatalf("committed SSE summary: %v %+v", err, point)
	}
	if item := patch(`{"title":" Renamed pinned conversation "}`, 200); !item.Pinned || item.Title != "Renamed pinned conversation" {
		t.Fatalf("rename lost pin: %+v", item)
	}
	if item := patch(`{"pinned":false}`, 200); item.Pinned || item.Title != "Renamed pinned conversation" {
		t.Fatalf("unpin response: %+v", item)
	}
	checkpoint, err := app.store.Checkpoint(context.Background(), id)
	if err != nil || checkpoint.Session.Pinned || checkpoint.Session.Title != "Renamed pinned conversation" {
		t.Fatalf("checkpoint metadata: %v %+v", err, checkpoint)
	}
}
