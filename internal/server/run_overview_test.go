package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"vista/internal/store"
)

func TestRunOverviewRouteReturnsSnapshotAndScopesToSession(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const session = "00000000-0000-4000-8000-000000000021"
	const other = "00000000-0000-4000-8000-000000000022"
	const run = "00000000-0000-4000-8000-000000000023"
	for _, id := range []string{session, other} {
		if _, err := st.Create(ctx, id, "overview"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := st.BeginRun(ctx, session, run, "user", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordModelRequest(ctx, session, run, "openai-chat", "test"); err != nil {
		t.Fatal(err)
	}
	last, err := st.AppendModelDelta(ctx, session, run, 1, 0, "reply")
	if err != nil {
		t.Fatal(err)
	}
	app := &App{store: st}
	request := func(id string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		app.session(response, httptest.NewRequest(http.MethodGet, "/api/sessions/"+id+"/runs/"+run+"/overview", nil))
		return response
	}
	response := request(session)
	var overview store.RunOverview
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &overview) != nil || overview.ModelCalls != 1 || overview.ThroughSequence != last.Sequence || len(overview.Events) != 1 || overview.Events[0].CreatedAt == "" {
		t.Fatalf("snapshot HTTP %d %+v body=%s", response.Code, overview, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("snapshot can be cached")
	}
	if response := request(other); response.Code != http.StatusNotFound {
		t.Fatalf("cross-session run: HTTP %d", response.Code)
	}
	for _, query := range []string{"after=-1", "after=abc", "after=", "after=9223372036854775808", "through=0", "through=-1", "through=no", "through=", "through=9223372036854775808", "after=2&through=2", "after=3&through=2", "after=0&after=1", "through=1&through=2"} {
		response := httptest.NewRecorder()
		app.session(response, httptest.NewRequest(http.MethodGet, "/api/sessions/"+session+"/runs/"+run+"/overview?"+query, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid query %q: HTTP %d %s", query, response.Code, response.Body.String())
		}
	}
	response = httptest.NewRecorder()
	app.session(response, httptest.NewRequest(http.MethodGet, "/api/sessions/"+session+"/runs/"+run+"/overview?after=0&through="+strconv.FormatInt(last.Sequence-1, 10), nil))
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &overview) != nil || overview.ModelCalls != 1 || overview.ThroughSequence != last.Sequence-1 || overview.FinishedAt == nil {
		t.Fatalf("bounded snapshot HTTP %d %+v body=%s", response.Code, overview, response.Body.String())
	}
}
