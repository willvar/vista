package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadSessionRouteAcknowledgesOnlyExistingReplies(t *testing.T) {
	app, session := inputQueueFixture(t)
	event, err := app.store.FinishRun(app.ctx, session, "run", "reply", "succeeded", "answer")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method string
		body   string
		status int
	}{
		{http.MethodGet, "", 405},
		{http.MethodPost, `{}`, 400},
		{http.MethodPost, `{"sequence":-1}`, 400},
		{http.MethodPost, `{"sequence":"wrong"}`, 400},
		{http.MethodPost, fmt.Sprintf(`{"sequence":%d}`, event.Sequence+100), 200},
	} {
		w := httptest.NewRecorder()
		app.session(w, httptest.NewRequest(tc.method, "/api/sessions/"+session+"/read", strings.NewReader(tc.body)))
		if w.Code != tc.status {
			t.Fatalf("%s %s: status %d, body %s", tc.method, tc.body, w.Code, w.Body.String())
		}
		if tc.status == 200 {
			var result struct{ ReadSequence int64 }
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.ReadSequence != event.Sequence {
				t.Fatalf("unexpected receipt: %s %v", w.Body.String(), err)
			}
		}
	}
}
