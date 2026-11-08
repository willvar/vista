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
