package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vista/internal/provider"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestRepeatedCompactionIncludesRuntimeReportsAtTail(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprintf("manual=%t", manual), func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			var evidence atomic.Uint32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct{ Messages []provider.ChatMessage }
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				for _, message := range request.Messages {
					for i := 0; i < 3; i++ {
						if strings.Contains(message.Content, fmt.Sprintf("REPORT_%d", i)) {
							evidence.Or(1 << i)
						}
					}
				}
				continuationAnswer(w, "Reports retained with their original task IDs and output references.")
			}))
			defer upstream.Close()
			command := continuationCommand(upstream.URL, "current question")
			if manual {
				command.Action = "compact"
			}
			messages := []provider.ChatMessage{{Role: "system", Content: "fixed rules"}, {Role: "system", Content: "additional fixed rules"}, {Role: "user", Content: command.Prompt}}
			latest := opaqueCompactionGroup("latest")
			messages = append(messages, latest...)
			for i := 0; i < 3; i++ {
				text := jsonTextPreview(textPrefix(strings.Repeat(fmt.Sprintf("REPORT_%d ", i), 2400), maxRuntimeReportBytes), 24<<10)
				messages = append(messages, runtimeNotice(workerproto.RuntimeEvent{ID: fmt.Sprintf("reports:%d", i), RunID: "reports", Recipient: "main", Kind: "agent", SourceID: fmt.Sprintf("main/child-%d", i), Status: "succeeded", Content: text, Output: &workerproto.OutputRef{RunID: "reports", Turn: int32(i + 1), Bytes: 21600}}))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			compactor := newExecutionCompactor(messages)
			for pass := 0; pass < 2; pass++ {
				before := provider.EstimateRequestTokens(ctx, messages, nil)
				next, changed, err := compactor.compact(ctx, command, messages, nil, func(workerproto.Result) error { return nil }, &runScope{}, 0)
				if err != nil || !changed || provider.EstimateRequestTokens(ctx, next, nil) >= before {
					t.Fatalf("pass %d failed to release report context: changed=%t err=%v", pass, changed, err)
				}
				if !reflect.DeepEqual(next[:3], messages[:3]) {
					t.Fatal("fixed rules or current input were compressed")
				}
				if !manual {
					if !reflect.DeepEqual(next[3:3+len(latest)], latest) {
						t.Fatal("runtime reports displaced the protected assistant/tool group")
					}
				}
				messages = next
			}
			for _, message := range messages {
				if message.RuntimeData {
					t.Fatal("runtime report remained permanently pinned after repeated compaction")
				}
			}
			if evidence.Load() != 7 {
				t.Fatalf("reports were discarded without reaching the summary: %03b", evidence.Load())
			}
		})
	}
}

func TestJoinedReportsCompactAndRemainReadable(t *testing.T) {
	for _, mode := range []string{"automatic", "manual"} {
		t.Run(mode, func(t *testing.T) {
			db, baseCtx := checkpointFixture(t)
			ctx, cancel := context.WithTimeout(baseCtx, 10*time.Second)
			defer cancel()
			var parentTurns, childTurns, summaries atomic.Int32
			var summarized atomic.Uint32
			var refs sync.Map
			full := make([]string, 3)
			for i := range full {
				full[i] = strings.Repeat(fmt.Sprintf("REPORT_%d verified evidence. ", i), 1000) + fmt.Sprintf("END_%d", i)
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct{ Messages []provider.ChatMessage }
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				if strings.HasPrefix(request.Messages[0].Content, executionSummaryPrompt) {
					summaries.Add(1)
					for _, message := range request.Messages {
						for i := range full {
							if strings.Contains(message.Content, fmt.Sprintf("REPORT_%d", i)) {
								summarized.Or(1 << i)
							}
						}
					}
					continuationAnswer(w, "Retained verified findings from the three required reports; use their original output references to read more.")
					return
				}
				latestUser := ""
				for _, message := range request.Messages {
					if message.Role == "user" {
						latestUser = message.Content
					}
				}
				for i := range full {
					if latestUser == fmt.Sprintf("child %d", i) {
						childTurns.Add(1)
						continuationAnswer(w, full[i])
						return
					}
				}
				switch parentTurns.Add(1) {
				case 1:
					var calls []provider.ToolCall
					for i := range full {
						arguments, _ := json.Marshal(map[string]string{"task": fmt.Sprintf("child %d", i)})
						calls = append(calls, scriptedToolCall(fmt.Sprintf("child-%d", i), "delegate", string(arguments)))
					}
					continuationToolCalls(w, calls...)
				case 2:
					continuationAnswer(w, "candidate")
				default:
					var raw bool
					for _, message := range request.Messages {
						raw = raw || message.RuntimeData || strings.Contains(message.Content, `"kind":"agent"`)
					}
					if mode == "automatic" && raw && summarized.Load() != 7 {
						w.WriteHeader(http.StatusBadRequest)
						fmt.Fprint(w, `{"error":{"code":"context_length_exceeded"}}`)
						return
					}
					continuationAnswer(w, "finished")
				}
			}))
			defer upstream.Close()
			user, _, err := db.BeginRun(ctx, "session", "joined", "user", "synthesize the required reports")
			if err != nil {
				t.Fatal(err)
			}
			command := continuationCommand(upstream.URL, user.Content)
			command.SessionID, command.RunID, command.MessageSequence = "session", "joined", user.Sequence
			var final store.ModelOutputRef
			sink := historyTestSink(db, command, &final)
			answer, err := Run(ctx, command, func(result workerproto.Result) error {
				if result.Kind == "agent_task" {
					if result.Depth > 0 && result.Task.Report != nil {
						refs.Store(result.Task.ID, *result.Task.Report)
					}
					_, err := db.RecordAgentTask(ctx, "session", result)
					return err
				}
				return sink(result)
			})
			if err != nil || answer != "finished" || childTurns.Load() != 3 {
				t.Fatalf("join run failed or repeated children: answer=%q children=%d err=%v", answer, childTurns.Load(), err)
			}
			if _, err := db.FinishRun(ctx, "session", "joined", "answer", "succeeded", answer, final); err != nil {
				t.Fatal(err)
			}
			if mode == "manual" {
				for attempt := 0; attempt < 4 && summarized.Load() != 7; attempt++ {
					through, _, err := db.BeginCompaction(ctx, "session", fmt.Sprintf("manual-%d", attempt))
					if err != nil {
						t.Fatal(err)
					}
					command.Action, command.RunID, command.MessageSequence = "compact", fmt.Sprintf("manual-%d", attempt), through
					if err := CompactSession(ctx, command, historyTestSink(db, command, &final)); err != nil {
						t.Fatal(err)
					}
					if _, err := db.FinishCompaction(ctx, "session", command.RunID, "succeeded", ""); err != nil {
						t.Fatal(err)
					}
				}
			}
			if summaries.Load() == 0 || summarized.Load() != 7 {
				t.Fatalf("reports did not participate in %s compaction: calls=%d evidence=%03b", mode, summaries.Load(), summarized.Load())
			}
			for i := range full {
				value, ok := refs.Load(fmt.Sprintf("main/child-%d", i))
				if !ok {
					t.Fatalf("report %d lost its original reference", i)
				}
				ref := value.(workerproto.OutputRef)
				var restored strings.Builder
				for offset := int64(0); offset < ref.Bytes; {
					arguments, _ := json.Marshal(map[string]any{"runId": ref.RunID, "turn": ref.Turn, "offset": offset})
					output, err := (&toolRuntime{parent: command}).readModelOutput(ctx, scriptedToolCall("read-report", "read_model_output", string(arguments)))
					var page store.ModelOutputPage
					if err != nil || json.Unmarshal([]byte(output), &page) != nil || page.NextOffset <= offset {
						t.Fatalf("cannot read compacted report %d at %d: %v", i, offset, err)
					}
					restored.WriteString(page.Content)
					offset = page.NextOffset
				}
				if restored.String() != full[i] {
					t.Fatalf("compaction altered report %d: got %d bytes want %d", i, restored.Len(), len(full[i]))
				}
			}
		})
	}
}
