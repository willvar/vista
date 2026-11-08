package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/workerproto"
)

func TestCompactionTriesOneRequestBeforeOverflowAndRecordsEachOutcome(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(fmt.Sprintf("overflow=%v", overflow), func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			messages := []provider.ChatMessage{
				{Role: "system", Content: "instructions"},
				{Role: "user", Content: "Keep the authorized task and evidence."},
				{Role: "assistant", Content: strings.Repeat("verified finding 中文\n", 18000) + "END_EVIDENCE"},
				{Role: "assistant", Content: "latest reply"},
			}
			transcript := executionTranscript(messages, []executionGroup{{2, 3}})
			const marker = "Completed execution transcript (possibly a continued segment):\n"
			var chunks []string
			requests, failures := 0, 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Input     []struct{ Role, Content string }
					Tools     []any
					Reasoning struct{ Effort string }
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Input) == 0 {
					t.Errorf("invalid summary request: %v", err)
					w.WriteHeader(400)
					return
				}
				_, chunk, found := strings.Cut(body.Input[len(body.Input)-1].Content, marker)
				if !found || len(body.Tools) != 0 || body.Reasoning.Effort != "high" {
					t.Error("summary changed effort, used tools or lost the transcript")
				}
				requests++
				if requests == 1 && chunk != transcript {
					t.Error("first summary request was needlessly prepartitioned")
				}
				if overflow && len(chunk) > len(transcript)/3 {
					failures++
					w.WriteHeader(400)
					fmt.Fprint(w, `{"error":{"code":"context_length_exceeded"}}`)
					return
				}
				chunks = append(chunks, chunk)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Retained task, authorization and verified evidence."}]}],"usage":{"input_tokens":123,"output_tokens":9}}}`+"\n\n")
			}))
			defer upstream.Close()
			command := workerproto.Command{Action: "compact", RunID: "efficiency", Model: modelcfg.Model{Provider: "openai-responses", ID: "fixture", BaseURL: upstream.URL, APIKey: "fixture", ReasoningEffort: "high"}}
			var events []workerproto.Result
			c := newExecutionCompactor(messages)
			next, changed, err := c.compact(context.Background(), command, messages, nil, func(r workerproto.Result) error { events = append(events, r); return nil }, &runScope{}, 0)
			if err != nil || !changed || provider.EstimateRequestTokens(context.Background(), next, nil) >= provider.EstimateRequestTokens(context.Background(), messages, nil) || strings.Join(chunks, "") != transcript {
				t.Fatalf("changed=%v requests=%d reconstruction=%v error=%v", changed, requests, strings.Join(chunks, "") == transcript, err)
			}
			if !overflow && requests != 1 || overflow && (failures != 2 || len(chunks) != 4) {
				t.Fatalf("requests=%d failures=%d successful chunks=%d", requests, failures, len(chunks))
			}
			pending := map[int32]bool{}
			completed := 0
			for _, event := range events {
				switch event.Kind {
				case "model_request":
					if event.Turn <= 0 || event.ModelUse.Purpose != "summary" || pending[event.Turn] {
						t.Fatal("summary request has no unique identity")
					}
					pending[event.Turn] = true
				case "model_result":
					result := event.ModelResult
					if !pending[event.Turn] || result.Purpose != "summary" || result.DurationMS <= 0 || result.ReasoningEffort != "high" {
						t.Fatal("summary outcome is missing identity, duration or effort")
					}
					if result.FinishReason != "failed" && (result.Usage == nil || result.Usage.InputTokens != 123 || result.Usage.OutputTokens != 9) {
						t.Fatal("reported summary usage was lost")
					}
					delete(pending, event.Turn)
					completed++
				}
			}
			if len(pending) != 0 || completed != requests {
				t.Fatalf("unpaired requests=%d outcomes=%d requests=%d", len(pending), completed, requests)
			}
		})
	}
}

func TestBalancedCompactionDoesNotCreateTinyRemainderRequests(t *testing.T) {
	for _, text := range []string{strings.Repeat("x", 404113), strings.Repeat("fact 中文🙂\n", 37001)} {
		parts := int64(4)
		var sizes []int
		for remaining := text; remaining != ""; {
			end := balancedTranscriptBoundary(remaining, parts)
			if end <= 0 {
				t.Fatal("summary chunk made no progress")
			}
			sizes = append(sizes, end)
			remaining = remaining[end:]
			parts--
		}
		if len(sizes) != 4 || sizes[len(sizes)-1] < len(text)/6 {
			t.Fatalf("unbalanced summary requests: %v", sizes)
		}
	}
}

func TestFailedSummaryReportsOutcomeAndRetainsOriginalContext(t *testing.T) {
	for _, kind := range []string{"HTTP error", "empty summary", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch kind {
				case "HTTP error":
					w.WriteHeader(http.StatusUnauthorized)
				case "empty summary":
					compactionResponse(w, []any{compactionText(" ")})
				case "cancelled":
					cancel()
				}
			}))
			defer upstream.Close()
			command := workerproto.Command{Action: "compact", RunID: "failed-summary", Model: modelcfg.Model{Provider: "openai-responses", ID: "fixture", BaseURL: upstream.URL, APIKey: "fixture"}}
			messages := []provider.ChatMessage{{Role: "system", Content: "instructions"}, {Role: "user", Content: "task"}, {Role: "assistant", Content: strings.Repeat("evidence ", 2000)}}
			c := newExecutionCompactor(messages)
			var outcome *workerproto.ModelResult
			next, changed, err := c.compact(ctx, command, messages, nil, func(r workerproto.Result) error {
				if r.Kind == "model_result" {
					outcome = r.ModelResult
				}
				return nil
			}, &runScope{}, 0)
			if err == nil || changed || !reflect.DeepEqual(next, messages) || c.summary != "" || outcome == nil || outcome.DurationMS <= 0 || outcome.Purpose != "summary" {
				t.Fatalf("changed=%v outcome=%+v error=%v", changed, outcome, err)
			}
			want := "failed"
			if kind == "cancelled" {
				want = "cancelled"
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			}
			if outcome.FinishReason != want {
				t.Fatalf("outcome=%s want=%s", outcome.FinishReason, want)
			}
		})
	}
}

func TestCompactionRetainsActualCurrentRulesAndNativePairs(t *testing.T) {
	for _, changedFile := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed=%v", changedFile), func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			dir := t.TempDir()
			path := filepath.Join(dir, "AGENTS.md")
			if err := os.WriteFile(path, []byte("Rules apply within this directory only."), 0600); err != nil {
				t.Fatal(err)
			}
			rules := instructionMessages(t, path, 0)
			rules[0].ResponseReasoning = []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"native-replay"}`)}
			if changedFile {
				if err := os.WriteFile(path, []byte("New rules must be read."), 0600); err != nil {
					t.Fatal(err)
				}
			}
			messages := append([]provider.ChatMessage{{Role: "system", Content: "instructions"}, {Role: "user", Content: "task"}}, rules...)
			messages = append(messages, provider.ChatMessage{Role: "assistant", Content: strings.Repeat("old verified evidence ", 8000)}, provider.ChatMessage{Role: "assistant", Content: "latest reply"})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				compactionResponse(w, []any{compactionText("Verified findings and pending task.")})
			}))
			defer upstream.Close()
			command := workerproto.Command{Action: "compact", RunID: "rules", Model: modelcfg.Model{Provider: "openai-responses", ID: "fixture", BaseURL: upstream.URL, APIKey: "fixture"}}
			c := newExecutionCompactor(messages)
			next, changed, err := c.compact(context.Background(), command, messages, nil, func(workerproto.Result) error { return nil }, &runScope{}, 0)
			if err != nil || !changed {
				t.Fatalf("changed=%v error=%v", changed, err)
			}
			state := projectInstructionState{}
			state.acknowledge(next)
			call := instructionCall(t, "file", "read_file", map[string]string{"path": filepath.Join(dir, "target.txt")})
			if changedFile {
				requireInstructionNotice(t, &state, call, path)
				return
			}
			requireInstructionNotice(t, &state, call)
			if !reflect.DeepEqual(next[2:4], rules) {
				t.Fatal("rule text or native replay was rewritten")
			}
			// Persist and reload through the real checkpoint codec, then rebuild
			// read coverage. A summary-only claim would not authorize this read.
			checkpoint, err := saveContextFile(command, next, "rules-*.gob")
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(filepath.Dir(checkpoint))
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			restored, err := loadContextFile(root, checkpoint, command)
			if err != nil || !reflect.DeepEqual(restored, next) {
				t.Fatalf("restoration changed context: %v", err)
			}
			state.acknowledge(restored)
			requireInstructionNotice(t, &state, call)
		})
	}
}
