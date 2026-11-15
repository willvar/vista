package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
)

func TestCodexCompatibleSessionAndFirstTurnStateRemainScoped(t *testing.T) {
	state := new(RequestState)
	ctx := WithRequestState(context.Background(), state)
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("compatible gateway path changed: %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["prompt_cache_key"] != "conversation" || r.Header.Get("session-id") != "conversation" {
			t.Error("missing stable cache/session identity")
		}
		seen = append(seen, r.Header.Get("x-codex-turn-state"))
		w.Header().Set("x-codex-turn-state", fmt.Sprintf("state-%d", len(seen)))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "openai-responses", Custom: true, Authentication: "bearer", APIKey: "owner-a", BaseURL: server.URL + "/v1", ID: "fixture", SessionID: "conversation", ResponsesRoute: modelcfg.ResponsesRouteCodexCompatible}
	call := func() {
		t.Helper()
		if _, err := ModelTurn(ctx, model, []ChatMessage{{Role: "user", Content: "fixture"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	call()
	call()
	call()
	state.Reset()
	call()
	model.APIKey = "owner-b"
	call()
	want := []string{"", "state-1", "state-1", "", ""}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Fatalf("routing lifecycle = %v, want %v", seen, want)
	}
}

func TestResponsesUnknownCustomRouteDoesNotInventCacheOrRoutingFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if _, exists := body["prompt_cache_key"]; exists {
			t.Error("sent unsupported cache key")
		}
		if r.Header.Get("session-id") != "" || r.Header.Get("x-codex-turn-state") != "" {
			t.Error("invented proprietary routing headers")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "openai-responses", Custom: true, Authentication: "none", BaseURL: server.URL + "/v1", ID: "fixture", SessionID: "conversation"}
	if _, err := OpenAIResponsesTurn(WithRequestState(context.Background(), new(RequestState)), model, []ChatMessage{{Role: "user", Content: "fixture"}}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestResponsesLateStateAndSideRequestsCannotContaminateMainTurn(t *testing.T) {
	state := new(RequestState)
	ctx := WithRequestState(context.Background(), state)
	model := modelcfg.Model{Provider: "openai-responses", ResponsesRoute: modelcfg.ResponsesRouteCodexCompatible, SessionID: "conversation", APIKey: "owner-a", ID: "fixture"}
	request := func() *http.Request { return httptest.NewRequest("POST", "http://gateway.invalid/v1/responses", nil) }
	first, err := prepareCodexRouting(ctx, model, request())
	if err != nil {
		t.Fatal(err)
	}
	state.Reset()
	first(&http.Response{StatusCode: 200, Header: http.Header{"X-Codex-Turn-State": {"stale"}}})
	next := request()
	observe, _ := prepareCodexRouting(ctx, model, next)
	if next.Header.Get("x-codex-turn-state") != "" {
		t.Fatal("late response seeded a new turn")
	}
	observe(&http.Response{StatusCode: 200, Header: http.Header{"X-Codex-Turn-State": {"main"}}})
	title := ForPurpose(model, PurposeTitle)
	if title.SessionID != "" || promptCacheKey(title) != "" {
		t.Fatal("title retained the main cache identity")
	}
	summary := ForPurpose(model, PurposeCompaction)
	if summary.SessionID == model.SessionID || promptCacheKey(summary) == promptCacheKey(model) {
		t.Fatal("summary shares the main cache series")
	}
	side := request()
	_, _ = prepareCodexRouting(ctx, summary, side)
	if side.Header.Get("x-codex-turn-state") != "" {
		t.Fatal("side request reused the main sticky state")
	}
	model.Headers = map[string]string{"session_id": "static-session"}
	if _, err := prepareCodexRouting(ctx, model, request()); err == nil {
		t.Fatal("static routing override accepted")
	}
	long := model
	long.SessionID = strings.Repeat("session", 30)
	long.Headers = nil
	if len(promptCacheKey(long)) != 64 || promptCacheKey(long) != promptCacheKey(long) {
		t.Fatal("long cache identity is not stable and bounded")
	}
}
