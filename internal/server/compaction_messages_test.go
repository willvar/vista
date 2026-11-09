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

	"github.com/google/uuid"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestCompactionSummaryAppearsInMessagePagesWithoutDuplicatingReplyResults(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sessionID, runID := uuid.NewString(), uuid.NewString()
	if _, err := st.Create(ctx, sessionID, "Summary"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.BeginRun(ctx, sessionID, runID, uuid.NewString(), "task"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendRunEvent(ctx, sessionID, runID, "model_result", workerproto.Result{
		RunID: runID, Turn: 1, ModelResult: &workerproto.ModelResult{Provider: "custom", ID: "model", FinishReason: "stop"},
	}); err != nil {
		t.Fatal(err)
	}
	notice, err := st.AppendRunEvent(ctx, sessionID, runID, "context_notice", map[string]any{
		"runId": runID, "depth": 0, "summary": "**Verified findings.**", "checkpoint": map[string]any{"path": "private-checkpoint.gob"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinishRun(ctx, sessionID, runID, uuid.NewString(), "succeeded", "answer"); err != nil {
		t.Fatal(err)
	}
	app := &App{store: st}
	for _, suffix := range []string{"/messages", fmt.Sprintf("/messages?after=%d", notice.Sequence-1), "/snapshot"} {
		t.Run(suffix, func(t *testing.T) {
			response := httptest.NewRecorder()
			app.session(response, httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+suffix, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("HTTP %d: %s", response.Code, response.Body)
			}
			var messages []messageWithResults
			body := response.Body.Bytes()
			if suffix == "/snapshot" {
				var snapshot struct {
					Messages json.RawMessage `json:"messages"`
				}
				if err := json.Unmarshal(body, &snapshot); err != nil {
					t.Fatal(err)
				}
				body = snapshot.Messages
			}
			if err := json.Unmarshal(body, &messages); err != nil {
				t.Fatal(err)
			}
			summaries, results := 0, 0
			for _, message := range messages {
				if message.ContextSummary {
					summaries++
					if message.Role != "assistant" || message.Content != "**Verified findings.**" || len(message.ModelResults) != 0 {
						t.Fatalf("invalid summary message: %+v", message)
					}
				}
				results += len(message.ModelResults)
			}
			if summaries != 1 || results != 1 || strings.Contains(string(body), "private-checkpoint.gob") {
				t.Fatalf("summary duplicated usage or exposed checkpoint internals: %s", body)
			}
		})
	}
}

func TestSummaryResultsStayWithinTheirCompactionAcrossSnapshotAndSingleMessagePages(t *testing.T) {
	app, sessionID := inputQueueFixture(t)
	ctx := app.ctx
	if err := app.store.RecordRunSelection(ctx, "run", "chat-model", "high", "endpoint"); err != nil {
		t.Fatal(err)
	}
	appendEvent := func(kind string, data any) store.Event {
		t.Helper()
		event, err := app.store.AppendRunEvent(ctx, sessionID, "run", kind, data)
		if err != nil {
			t.Fatal(err)
		}
		return *event
	}
	result := func(turn int32, purpose string, depth int) {
		appendEvent("model_result", workerproto.Result{RunID: "run", Turn: turn, Depth: depth,
			ModelResult: &workerproto.ModelResult{Provider: "custom", ID: "actual-model", Purpose: purpose, FinishReason: "stop", DurationMS: 123,
				Usage: &workerproto.ModelUsage{InputTokens: int64(turn), OutputTokens: 1}}})
	}
	result(1, "", 0)
	var notices []store.Event
	for i := 0; i < 2; i++ {
		appendEvent("context_status", map[string]any{"runId": "run", "phase": "compacting"})
		appendEvent("model_request", map[string]any{"runId": "run", "purpose": "summary", "key": fmt.Sprintf("summary-connection-%d", i), "provider": "custom", "id": "actual-model"})
		result(int32(i*3+2), "summary", 0)
		result(int32(i*3+3), "summary", 1)
		result(int32(i*3+4), "summary", 0)
		notices = append(notices, appendEvent("context_notice", map[string]any{"runId": "run", "depth": 0, "summary": fmt.Sprintf("Summary %d", i)}))
	}
	result(8, "", 0)
	if _, err := app.store.FinishRun(ctx, sessionID, "run", "answer", "succeeded", "Answer"); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"/messages", "/snapshot", fmt.Sprintf("/messages?before=%d", notices[1].Sequence+1), fmt.Sprintf("/messages?after=%d", notices[0].Sequence)} {
		response := httptest.NewRecorder()
		app.session(response, httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+suffix, nil))
		if response.Code != 200 {
			t.Fatalf("%s: %d %s", suffix, response.Code, response.Body.String())
		}
		body := response.Body.Bytes()
		if suffix == "/snapshot" {
			var snapshot struct {
				Messages json.RawMessage `json:"messages"`
			}
			if err := json.Unmarshal(body, &snapshot); err != nil {
				t.Fatal(err)
			}
			body = snapshot.Messages
		}
		var page []messageWithResults
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		for _, message := range page {
			if message.Role != "assistant" {
				continue
			}
			if message.ContextSummary {
				if len(message.ModelResults) != 2 || message.Provider != "custom" || message.ModelID != "actual-model" || message.ResultsAfter <= 0 {
					t.Fatalf("summary lost scoped usage or identity: %+v", message)
				}
				wantFirst := int32(2)
				wantKey := "summary-connection-0"
				if message.Sequence == notices[1].Sequence {
					wantFirst = 5
					wantKey = "summary-connection-1"
				}
				if message.ModelKey != wantKey {
					t.Fatalf("summary used another compaction's connection: got %q, want %q", message.ModelKey, wantKey)
				}
				if message.ModelResults[0].Turn != wantFirst || message.ModelResults[1].Turn != wantFirst+2 {
					t.Fatalf("summary includes another compaction: %+v", message.ModelResults)
				}
				for _, result := range message.ModelResults {
					if result.ReasoningEffort != "" {
						t.Fatal("summary inherited the foreground reasoning setting")
					}
				}
			} else if len(message.ModelResults) != 4 {
				t.Fatalf("ordinary answer lost calls or duplicated root summary usage: %+v", message.ModelResults)
			}
		}
	}
	// A page containing just a summary must load its results without a formal
	// assistant message from that run being present on the same page.
	items, err := app.store.DisplayMessagesAfter(ctx, sessionID, notices[0].Sequence, 1)
	if err != nil || len(items) != 1 || !items[0].ContextSummary {
		t.Fatalf("summary-only page: %+v %v", items, err)
	}
	page, err := app.messagesWithResults(httptest.NewRequest(http.MethodGet, "/", nil), sessionID, items)
	if err != nil || len(page[0].ModelResults) != 2 || page[0].ModelResults[0].Turn != 5 || page[0].ModelKey != "summary-connection-1" {
		t.Fatalf("summary-only usage: %+v %v", page, err)
	}
}
