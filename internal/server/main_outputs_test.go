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

func TestMainOutputsRouteOwnershipAndPagination(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	const session = "00000000-0000-4000-8000-000000000001"
	const run = "00000000-0000-4000-8000-000000000002"
	const other = "00000000-0000-4000-8000-000000000003"
	for _, id := range []string{session, other} {
		if _, err := s.Create(ctx, id, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.BeginRun(ctx, session, run, "input", "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendModelDelta(ctx, session, run, 1, 0, "Progress"); err != nil {
		t.Fatal(err)
	}
	app := &App{store: s}
	path := "/api/sessions/" + session + "/runs/" + run + "/main-outputs"
	for _, check := range []struct {
		path string
		code int
	}{{path, 200}, {path + "?before=1", 200}, {path + "?before=0", 400}, {path + "?before=-1", 400}, {path + "?before=x", 400}, {path + "?before=2&before=3", 400}, {"/api/sessions/" + other + "/runs/" + run + "/main-outputs", 404}} {
		response := httptest.NewRecorder()
		app.session(response, httptest.NewRequest(http.MethodGet, check.path, nil))
		if response.Code != check.code {
			t.Fatalf("%s: %d %s", check.path, response.Code, response.Body.String())
		}
		if check.path == path {
			var outputs []store.MainOutput
			if err := json.Unmarshal(response.Body.Bytes(), &outputs); err != nil || len(outputs) != 1 || outputs[0].Bytes != 8 || outputs[0].Complete || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("index response: %+v %v", outputs, err)
			}
		}
	}
}
