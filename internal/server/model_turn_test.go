package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"vista/internal/store"
)

func TestModelOutputRouteAcceptsTurn257AndRetainsInt32Bounds(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const sessionID = "00000000-0000-4000-8000-000000000001"
	const runID = "00000000-0000-4000-8000-000000000002"
	if _, err := st.Create(ctx, sessionID, "many turns"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.BeginRun(ctx, sessionID, runID, "input", "hello"); err != nil {
		t.Fatal(err)
	}
	const content = "Completed on turn 257"
	if _, err := st.AppendModelDelta(ctx, sessionID, runID, 257, 0, content); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinishRun(ctx, sessionID, runID, "answer", "succeeded", content, store.ModelOutputRef{Turn: 257, Bytes: len(content)}); err != nil {
		t.Fatal(err)
	}
	app := &App{store: st}
	path := "/api/sessions/" + sessionID + "/runs/" + runID + "/model-output?turn="
	response := httptest.NewRecorder()
	app.session(response, httptest.NewRequest(http.MethodGet, path+"257", nil))
	var page store.ModelOutputPage
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Turn != 257 || !page.Complete || page.Content != content {
		t.Fatalf("model output route: HTTP %d page=%+v", response.Code, page)
	}
	for _, invalid := range []string{"0", "-1", "2147483648"} {
		response := httptest.NewRecorder()
		app.session(response, httptest.NewRequest(http.MethodGet, path+invalid, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("turn %s: expected HTTP 400, got %d", invalid, response.Code)
		}
	}
}
