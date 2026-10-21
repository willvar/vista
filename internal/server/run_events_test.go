package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestRunEventRoutesReturnUntruncatedBodiesWithoutToken(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const session = "00000000-0000-4000-8000-000000000041"
	const otherSession = "00000000-0000-4000-8000-000000000042"
	const run = "00000000-0000-4000-8000-000000000043"
	const otherRun = "00000000-0000-4000-8000-000000000044"
	for _, id := range []string{session, otherSession} {
		if _, err := st.Create(ctx, id, "events"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := st.BeginRun(ctx, session, run, "input", "test"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.BeginRun(ctx, otherSession, otherRun, "other-input", "private"); err != nil {
		t.Fatal(err)
	}
	var target []store.Event
	for _, kind := range []string{"model_request", "model_reasoning_delta", "model_delta", "tool"} {
		data := workerproto.Result{RunID: run, Kind: kind, Turn: 300, TaskID: "main/child", ParentTaskID: "main", Depth: 1}
		if kind == "model_request" {
			data.ModelUse = &workerproto.ModelUse{Provider: "openai-chat", ID: "model"}
		} else {
			data.Content = "fragment"
		}
		if kind == "tool" {
			data.Status = "succeeded"
			data.Tool = &workerproto.ToolProgress{CallID: "call-body", Name: "shell"}
			data.Content = strings.Repeat("x", 18000)
		}
		event, err := st.AppendRunEvent(ctx, session, run, kind, data)
		if err != nil {
			t.Fatal(err)
		}
		target = append(target, *event)
	}
	app := &App{store: st}
	handler := app.handler()
	request := func(method, path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		input := httptest.NewRequest(method, "http://127.0.0.1:5186"+path, nil)
		handler.ServeHTTP(response, input)
		return response
	}
	root := "/api/sessions/" + session + "/runs/" + run
	exact := fmt.Sprintf("%s/events/%d", root, target[3].Sequence)
	turns := root + "/turns/300/events"
	for _, path := range []string{exact, turns} {
		if response := request(http.MethodPost, path); response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("read-only %s: HTTP %d", path, response.Code)
		}
	}
	response := request(http.MethodGet, exact)
	var recovered store.Event
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &recovered) != nil || string(recovered.Data) != string(target[3].Data) || recovered.CreatedAt != target[3].CreatedAt {
		t.Fatalf("untruncated event HTTP %d: %+v", response.Code, recovered)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("original event may be cached")
	}
	response = request(http.MethodGet, turns)
	var page []store.Event
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page) != 4 {
		t.Fatalf("turn page HTTP %d: %s", response.Code, response.Body.String())
	}
	for index, event := range page {
		if event.Sequence != target[index].Sequence {
			t.Fatalf("non-chronological page: %+v", page)
		}
	}
	response = request(http.MethodGet, fmt.Sprintf("%s?before=%d", turns, target[3].Sequence))
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page) != 3 || page[2].Sequence != target[2].Sequence {
		t.Fatalf("exclusive before: HTTP %d %s", response.Code, response.Body.String())
	}
	for _, path := range []string{
		"/api/sessions/" + otherSession + "/runs/" + run + "/events/2",
		"/api/sessions/" + otherSession + "/runs/" + run + "/turns/300/events",
		"/api/sessions/" + session + "/runs/" + otherRun + "/events/2",
		"/api/sessions/" + session + "/runs/" + otherRun + "/turns/300/events",
		root + "/events/999999",
	} {
		if response := request(http.MethodGet, path); response.Code != http.StatusNotFound {
			t.Fatalf("scope isolation %s: HTTP %d", path, response.Code)
		}
	}
	for _, value := range []string{"0", "-1", "bad", "9223372036854775808"} {
		if response := request(http.MethodGet, root+"/events/"+value); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid sequence %s: HTTP %d", value, response.Code)
		}
	}
	for _, value := range []string{"0", "-1", "bad", "2147483648"} {
		if response := request(http.MethodGet, root+"/turns/"+value+"/events"); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid turn %s: HTTP %d", value, response.Code)
		}
	}
	for _, value := range []string{"", "0", "-1", "bad", "9223372036854775808"} {
		if response := request(http.MethodGet, turns+"?before="+value); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid before %q: HTTP %d", value, response.Code)
		}
	}
	response = request(http.MethodGet, root+"/turns/2147483647/events")
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != "[]" {
		t.Fatalf("valid large turn rejected: HTTP %d %s", response.Code, response.Body.String())
	}
}
