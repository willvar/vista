package provider

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
	modelcfg "vista/internal/model"
	"vista/internal/modeltrace"
)

func TestModelDiagnosticsCaptureParserFailureWithReasoningCounters(t *testing.T) {
	dir := configureTestUserAgent(t)
	const secret = "diagnostic-fixture-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("wrong upstream authentication")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for range 400 {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"reasoning":"x","reasoning_details":[{"type":"reasoning.text","index":0,"text":"x"}]}}]}`+"\n\n")
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"reasoning_details":[{"type":"unexpected","text":"broken"}]}}]}`+"\n\n")
	}))
	defer server.Close()
	scope := modeltrace.Scope{Enabled: true, SessionID: uuid.NewString(), RunID: uuid.NewString(), Turn: 7, TaskID: "main/sub", Purpose: "conversation"}
	ctx := modeltrace.WithScope(context.Background(), scope)
	_, err := ModelTurnStreamWithReasoning(ctx, modelcfg.Model{Provider: "openai-chat", ID: "fixture", APIKey: secret, BaseURL: server.URL}, []ChatMessage{{Role: "user", Content: "fixture prompt"}}, nil, nil, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "unsupported model reasoning detail") {
		t.Fatalf("parser result: %v", err)
	}
	list, err := modeltrace.List(filepath.Join(dir, "state"), scope.SessionID, scope.RunID)
	if err != nil || len(list) != 1 {
		t.Fatalf("failed call diagnostics: %+v %v", list, err)
	}
	record, err := modeltrace.Read(filepath.Join(dir, "state"), scope.SessionID, scope.RunID, list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Parsing == nil || record.Parsing.Event != 401 || record.Parsing.Line != 802 || record.Parsing.ReasoningFragments != 401 || record.Parsing.ReasoningBlocks != 1 || record.Parsing.VisibleReasoningBytes != 400 || len(record.Parsing.ReasoningFields) != 2 {
		t.Fatalf("parser position/counters: %+v", record.Parsing)
	}
	if record.Turn != 7 || record.TaskID != "main/sub" || len(record.Exchanges) != 1 || record.Exchanges[0].Status != 200 || !strings.Contains(record.Exchanges[0].Response.Content, `"type":"unexpected"`) || !strings.Contains(record.Exchanges[0].Request.Content, "fixture prompt") {
		t.Fatalf("lost request or failure response: %+v", record)
	}
	encoded, _ := json.Marshal(record)
	if strings.Contains(string(encoded), secret) {
		t.Fatal("record leaked model key")
	}
}

func TestModelDiagnosticsSharedAcrossProtocolsAndDisabledByDefault(t *testing.T) {
	dir := configureTestUserAgent(t)
	const secret = "fixture-protocol-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":{"message":"rejected %s"}}`, secret)
	}))
	defer server.Close()
	for _, protocol := range []string{"openai-chat", "openai-responses", "anthropic", "gemini", "cohere", "mistral"} {
		t.Run(protocol, func(t *testing.T) {
			scope := modeltrace.Scope{Enabled: true, SessionID: uuid.NewString(), RunID: uuid.NewString(), Purpose: "summary"}
			model := modelcfg.Model{Provider: protocol, ID: "fixture", APIKey: secret, BaseURL: server.URL}
			_, err := ModelTurnStreamWithReasoning(modeltrace.WithScope(context.Background(), scope), model, []ChatMessage{{Role: "user", Content: "fixture"}}, nil, nil, func(string) error { return nil })
			if err == nil {
				t.Fatal("upstream error hidden")
			}
			list, err := modeltrace.List(filepath.Join(dir, "state"), scope.SessionID, scope.RunID)
			if err != nil || len(list) != 1 {
				t.Fatalf("%s capture: %+v %v", protocol, list, err)
			}
			record, err := modeltrace.Read(filepath.Join(dir, "state"), scope.SessionID, scope.RunID, list[0].ID)
			if err != nil || len(record.Exchanges) != 1 || record.Exchanges[0].Status != 401 || !strings.Contains(record.Exchanges[0].Response.Content, "[redacted]") || record.Protocol != protocol || record.Purpose != "summary" {
				t.Fatalf("protocol record: %+v %v", record, err)
			}
			encoded, _ := json.Marshal(record)
			if strings.Contains(string(encoded), secret) {
				t.Fatal("protocol record leaked credentials")
			}
			scope.Enabled = false
			scope.RunID = uuid.NewString()
			ModelTurnStreamWithReasoning(modeltrace.WithScope(context.Background(), scope), model, []ChatMessage{{Role: "user", Content: "off"}}, nil, nil, func(string) error { return nil })
			if list, err := modeltrace.List(filepath.Join(dir, "state"), scope.SessionID, scope.RunID); err != nil || len(list) != 0 {
				t.Fatalf("disabled protocol was captured: %+v %v", list, err)
			}
		})
	}
}
