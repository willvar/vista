package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"vista/internal/appdirs"
	modelcfg "vista/internal/model"
	"vista/internal/modeltrace"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestResponsesRoutingKeepsToolRetryStateAndResetsForDeliveredInput(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	file := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(file, []byte("fixture evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	inputs := make(chan workerproto.Command, 1)
	var states, requests []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("session-id") != "wire-conversation" {
			t.Error("logical session identity was lost")
		}
		states = append(states, r.Header.Get("x-codex-turn-state"))
		requests = append(requests, r.Header.Get("x-client-request-id"))
		switch len(states) {
		case 1:
			arguments, _ := json.Marshal(map[string]string{"path": file})
			w.Header().Set("x-codex-turn-state", "first-state")
			compactionResponse(w, []any{map[string]any{"type": "function_call", "id": "read-item", "call_id": "read-call", "name": "read_file", "arguments": string(arguments)}})
		case 2:
			w.Header().Set("x-codex-turn-state", "rejected-state")
			http.Error(w, `{"error":{"code":"server_is_overloaded","message":"retry fixture"}}`, http.StatusServiceUnavailable)
		case 3:
			inputs <- workerproto.Command{RunID: "routing-run", InputID: "next-user", Prompt: "Follow up"}
			w.Header().Set("x-codex-turn-state", "later-state")
			compactionResponse(w, []any{compactionText("first reply")})
		case 4:
			w.Header().Set("x-codex-turn-state", "new-user-state")
			compactionResponse(w, []any{compactionText("done")})
		default:
			t.Error("unexpected request or repeated tool execution")
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer upstream.Close()
	command := workerproto.Command{RunID: "routing-run", Prompt: "Read the fixture", Model: modelcfg.Model{Provider: "openai-responses", ID: "fixture", APIKey: "fixture", SessionID: "wire-conversation", BaseURL: upstream.URL + "/v1", ResponsesRoute: modelcfg.ResponsesRouteCodexCompatible}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tools := 0
	answer, err := RunWithInputs(ctx, command, func(result workerproto.Result) error {
		if result.Kind == "tool" && result.Status == "started" {
			tools++
		}
		return nil
	}, inputs)
	if err != nil || answer != "done" || tools != 1 {
		t.Fatalf("routing recovery repeated work or lost input: answer=%q tools=%d error=%v", answer, tools, err)
	}
	if !reflect.DeepEqual(states, []string{"", "first-state", "first-state", ""}) {
		t.Fatalf("logical turn routing = %v", states)
	}
	if requests[0] == "" || requests[0] != requests[1] || requests[1] != requests[2] || requests[2] == requests[3] {
		t.Fatal("request correlation did not follow logical user turn boundaries")
	}
}

func TestResponsesSessionPreservesCacheBaselineAcrossUserTurns(t *testing.T) {
	db, ctx := checkpointFixture(t)
	sessionID := uuid.NewString()
	if _, err := db.Create(ctx, sessionID, "cache baseline fixture"); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	requestIDs := make(chan string, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if call > 2 {
			t.Error("unexpected extra model request")
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		if r.Header.Get("session-id") != sessionID || r.Header.Get("x-codex-turn-state") != "" {
			t.Error("new user turn lost its session identity or reused the previous routing token")
		}
		requestIDs <- r.Header.Get("x-client-request-id")
		cached := 0
		if call == 1 {
			cached = 80
		}
		body, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{
			"status": "completed", "output": []any{compactionText("ok")},
			"usage": map[string]any{"input_tokens": 100, "output_tokens": 1, "input_tokens_details": map[string]any{"cached_tokens": cached}},
		}})
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-codex-turn-state", "previous-turn-token")
		fmt.Fprintf(w, "data: %s\n\n", body)
	}))
	defer upstream.Close()
	session := NewSession(ctx)
	defer session.Close()
	stateDir, err := appdirs.State()
	if err != nil {
		t.Fatal(err)
	}
	var observations []modeltrace.CacheObservation
	var counts []int64
	for index := range 2 {
		runID := uuid.NewString()
		user, _, err := db.BeginRun(ctx, sessionID, runID, uuid.NewString(), fmt.Sprintf("ordinary input %d", index+1))
		if err != nil {
			t.Fatal(err)
		}
		command := workerproto.Command{SessionID: sessionID, RunID: runID, MessageSequence: user.Sequence, Prompt: user.Content, Debug: true,
			Model: modelcfg.Model{Provider: "openai-responses", ID: "fixture", APIKey: "synthetic", SessionID: sessionID, BaseURL: upstream.URL + "/v1", ResponsesRoute: modelcfg.ResponsesRouteCodexCompatible}}
		var final store.ModelOutputRef
		sink := historyTestSink(db, command, &final)
		emit := func(result workerproto.Result) error {
			if result.Kind == "model_diagnostic" {
				record, err := modeltrace.Read(stateDir, sessionID, runID, result.DiagnosticID)
				if err != nil {
					return err
				}
				if record.Cache == nil {
					return fmt.Errorf("missing cache diagnostics")
				}
				observations = append(observations, *record.Cache)
			}
			if result.Kind == "model_result" && result.ModelResult != nil && result.ModelResult.Usage != nil {
				counts = append(counts, result.ModelResult.Usage.CacheReadTokens)
			}
			return sink(result)
		}
		answer, err := session.Run(ctx, command, emit, nil, nil)
		if err != nil || answer != "ok" {
			t.Fatalf("run %d answer=%q error=%v", index+1, answer, err)
		}
		if _, err := db.FinishRun(ctx, sessionID, runID, uuid.NewString(), "succeeded", answer, final); err != nil {
			t.Fatal(err)
		}
	}
	if len(observations) != 2 || calls.Load() != 2 || !reflect.DeepEqual(counts, []int64{80, 0}) {
		t.Fatalf("diagnostics=%+v raw usage=%v requests=%d", observations, counts, calls.Load())
	}
	if observations[0].PrefixChange != "new-series" || observations[0].CachedDrop || observations[1].PrefixChange != "append-only" || !observations[1].CachedDrop {
		t.Fatalf("lost ordinary user turn comparison baseline: %+v", observations)
	}
	first, next := <-requestIDs, <-requestIDs
	if first == "" || next == "" || first == next {
		t.Fatal("ordinary user turns did not receive distinct request correlation IDs")
	}
}
