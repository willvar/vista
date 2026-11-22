package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vista/internal/configstore"
	"vista/internal/store"
)

func TestLegacyArchivedProfileRemainsAccessible(t *testing.T) {
	const id = "a86841e1-5873-4dc4-a937-239be5fe59a4"
	const sessionID = "b86841e1-5873-4dc4-a937-239be5fe59a4"
	const registry = `[{"id":"default","name":"","archived":false},{"id":"` + id + `","name":"Research","archived":true}]`
	ctx := t.Context()
	app := &App{ctx: ctx, configDir: t.TempDir()}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Chmod(app.configDir, 0700))
	must(configstore.Write(app.configDir, configstore.Profiles, []byte(registry)))
	p, err := openProfiles(app)
	must(err)
	t.Cleanup(p.close)
	must(p.createDirectory(p.configDir(id)))
	state := filepath.Join(p.configDir(id), "state")
	must(os.MkdirAll(state, 0700))
	st, err := store.Open(filepath.Join(state, "vista.db"))
	must(err)
	_, err = st.Create(ctx, sessionID, "Existing history")
	must(err)
	must(st.SetOpen(ctx, sessionID, false)) // No Worker needed for history.
	must(st.Close())

	call := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != want {
			t.Fatalf("%s %s: got %d, want %d (%s)", method, path, w.Code, want, w.Body)
		}
		return w
	}
	w := call(http.MethodGet, "/api/profiles", "", http.StatusOK)
	var items []profile
	must(json.Unmarshal(w.Body.Bytes(), &items))
	if len(items) != 2 || items[1].ID != id || items[1].Name != "Research" || strings.Contains(w.Body.String(), `"archived"`) {
		t.Fatalf("unexpected registry body: %s", w.Body)
	}
	base := "/profiles/" + id
	call(http.MethodGet, base+"/", "", http.StatusOK)
	if p.apps[id] != nil {
		t.Fatal("list and document reads must not open the Profile")
	}
	w = call(http.MethodGet, base+"/api/sessions", "", http.StatusOK)
	var sessions []store.Session
	must(json.Unmarshal(w.Body.Bytes(), &sessions))
	if len(sessions) != 1 || sessions[0].ID != sessionID || sessions[0].Title != "Existing history" || sessions[0].Open {
		t.Fatalf("prior history or its lifecycle changed: %+v", sessions)
	}
	before := p.apps[id]
	for _, request := range []string{`{"archived":true}`, `{"archived":false}`, `{"name":"Other","archived":true}`} {
		call(http.MethodPatch, "/api/profiles/"+id, request, http.StatusBadRequest)
	}
	if p.apps[id] != before || before.app.ctx.Err() != nil {
		t.Fatal("obsolete archive request stopped the Profile")
	}
	saved, _, err := configstore.Read(app.configDir, configstore.Profiles)
	must(err)
	if string(saved) != registry {
		t.Fatal("reading the legacy registry or rejecting a request rewrote it")
	}
	call(http.MethodPatch, "/api/profiles/"+id, `{"name":"Writing"}`, http.StatusOK)
	saved, _, err = configstore.Read(app.configDir, configstore.Profiles)
	must(err)
	if strings.Contains(string(saved), `"archived"`) || !strings.Contains(string(saved), `"name":"Writing"`) {
		t.Fatalf("rename did not write the simplified registry: %s", saved)
	}
}
