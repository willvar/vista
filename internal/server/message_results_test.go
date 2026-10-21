package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestMessagePageIncludesOnlyItsRunModelResults(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const sessionID = "00000000-0000-4000-8000-000000000001"
	if _, err := st.Create(ctx, sessionID, "results"); err != nil {
		t.Fatal(err)
	}
	for index, runID := range []string{"run-1", "run-2"} {
		if _, _, err := st.BeginRun(ctx, sessionID, runID, "user-"+runID, "Hi"); err != nil {
			t.Fatal(err)
		}
		result := workerproto.Result{RunID: runID, Kind: "model_result", TaskID: "main/child", ParentTaskID: "main", Depth: 1, Turn: 1, ModelResult: &workerproto.ModelResult{Provider: "openai-responses", ID: "gpt-6-luna", Purpose: "summary", FinishReason: "completed", DurationMS: float64(500 + index*100), ReasoningEffort: []string{"high", ""}[index], Usage: &workerproto.ModelUsage{InputTokens: int64(100 + index)}}}
		if err := st.RecordRunSelection(ctx, runID, "gpt-6-luna", "none", "endpoint"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.AppendRunEvent(ctx, sessionID, runID, "model_result", result); err != nil {
			t.Fatal(err)
		}
		if _, err := st.FinishRun(ctx, sessionID, runID, "answer-"+runID, "succeeded", "Hello"); err != nil {
			t.Fatal(err)
		}
	}
	app := &App{store: st}
	response := httptest.NewRecorder()
	app.messages(response, httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/messages", nil), sessionID)
	var messages []struct {
		RunID        string             `json:"runId"`
		Role         string             `json:"role"`
		ModelResults []modelResultEvent `json:"modelResults"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &messages) != nil || len(messages) != 4 {
		t.Fatalf("message page: %d %s", response.Code, response.Body.String())
	}
	for index, message := range messages {
		if index%2 == 0 {
			if len(message.ModelResults) != 0 {
				t.Fatalf("user message received model usage: %+v", message)
			}
			continue
		}
		if len(message.ModelResults) != 1 || message.ModelResults[0].RunID != message.RunID || message.ModelResults[0].Usage.InputTokens != int64(100+index/2) || message.ModelResults[0].DurationMS != float64(500+index/2*100) || message.ModelResults[0].ReasoningEffort != []string{"high", "none"}[index/2] || message.ModelResults[0].Purpose != "summary" || message.ModelResults[0].CreatedAt == "" || message.ModelResults[0].TaskID != "main/child" || message.ModelResults[0].ParentTaskID != "main" || message.ModelResults[0].Depth != 1 {
			t.Fatalf("model usage attached to wrong answer: %+v", message)
		}
	}
}

func TestSameRunRepliesPartitionUsageEvenAcrossMessagePages(t *testing.T) {
	app, sessionID := inputQueueFixture(t)
	ctx := app.ctx
	addResult := func(turn int32, depth int) {
		t.Helper()
		result := workerproto.Result{RunID: "run", Kind: "model_result", Turn: turn, Depth: depth, ModelResult: &workerproto.ModelResult{Provider: "openai-chat", ID: "model", FinishReason: "stop", Usage: &workerproto.ModelUsage{InputTokens: int64(turn * 10)}}}
		if _, err := app.store.AppendRunEvent(ctx, sessionID, "run", "model_result", result); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.store.AppendModelDelta(ctx, sessionID, "run", 1, 0, "First answer"); err != nil {
		t.Fatal(err)
	}
	addResult(1, 0)
	if _, _, err := app.store.QueueInput(ctx, sessionID, "follow-up", "now", "Next question"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.DeliverInput(ctx, sessionID, "run", "follow-up", "question-2", store.CompletedReply{ID: "answer-1", Content: "First answer", Output: store.ModelOutputRef{Turn: 1, Bytes: len("First answer")}}); err != nil {
		t.Fatal(err)
	}
	addResult(2, 1)
	addResult(3, 0)
	if _, err := app.store.FinishRun(ctx, sessionID, "run", "answer-2", "succeeded", "Second answer"); err != nil {
		t.Fatal(err)
	}
	items, err := app.store.MessagesBefore(ctx, sessionID, 0, 10)
	if err != nil || len(items) != 4 {
		t.Fatalf("messages: %+v %v", items, err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	page, err := app.messagesWithResults(request, sessionID, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(page[1].ModelResults) != 1 || page[1].ModelResults[0].Turn != 1 || len(page[3].ModelResults) != 2 || page[3].ModelResults[0].Turn != 2 || page[3].ModelResults[1].Turn != 3 {
		t.Fatalf("usage was duplicated or omitted: %+v", page)
	}
	// The earlier answer is not loaded on this page. Its usage still belongs to
	// that answer, including when the result events are replayed by the client.
	lastPage, err := app.messagesWithResults(request, sessionID, items[3:])
	if err != nil || len(lastPage) != 1 || lastPage[0].ResultsAfter != items[1].Sequence || len(lastPage[0].ModelResults) != 2 {
		t.Fatalf("reply boundary lost across pages: %+v %v", lastPage, err)
	}
}

func TestStatusNamesExistingCustomDefaultWithoutChangingItsKey(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	app := &App{store: st, started: time.Now(), models: modelCatalog{Default: "custom-gpt-6-astra", Models: []configuredModel{{Key: "custom-gpt-6-astra", Name: "Herdex", Provider: "openai-responses", ID: "gpt-6-astra"}}}}
	response := httptest.NewRecorder()
	app.status(response, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var status struct {
		Provider        string `json:"provider"`
		DefaultModel    string `json:"defaultModel"`
		DefaultModelKey string `json:"defaultModelKey"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &status) != nil || status.Provider != "Herdex" || status.DefaultModel != "Herdex/gpt-6-astra" || status.DefaultModelKey != "custom-gpt-6-astra" {
		t.Fatalf("custom default status: %d %s", response.Code, response.Body.String())
	}
}

func TestSessionManagerRouteSurvivesReload(t *testing.T) {
	app := &App{}
	response := httptest.NewRecorder()
	app.root(response, httptest.NewRequest(http.MethodGet, "/sessions", nil))
	if response.Code != http.StatusOK || response.Body.Len() == 0 {
		t.Fatalf("manager route: %d %s", response.Code, response.Body.String())
	}
}
