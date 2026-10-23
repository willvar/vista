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
	"strings"
	"sync/atomic"
	"testing"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/question"
	"vista/internal/resources"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func checkpointFixture(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	dir := filepath.Join(root, "vista", "state")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if _, err := db.Create(ctx, "session", "context fixture"); err != nil {
		t.Fatal(err)
	}
	return db, ctx
}

func persistCheckpoint(db *store.Store, ctx context.Context, command workerproto.Command, checkpoint *workerproto.ContextCheckpoint) error {
	_, err := db.AppendRunEvent(ctx, command.SessionID, command.RunID, "context_notice", map[string]any{
		"runId": command.RunID, "depth": 0, "checkpoint": checkpoint,
		"context": workerproto.CompactionStatus{RunID: command.RunID, Phase: "compacted", Manual: command.Action == "compact"},
	})
	return err
}

func TestCheckpointRetainsOpaquePairsAndAppendsHistoryExactlyOnce(t *testing.T) {
	db, ctx := checkpointFixture(t)
	user, _, err := db.BeginRun(ctx, "session", "run", "user", "original task")
	if err != nil {
		t.Fatal(err)
	}
	command := workerproto.Command{SessionID: "session", RunID: "run", MessageSequence: user.Sequence, Model: modelcfg.Model{Provider: "openai-responses", ProviderID: "relay", ID: "model"}}
	messages := append([]provider.ChatMessage{{Role: "system", Content: "old system"}, {Role: "assistant", Content: executionSummaryPrefix + "saved facts"}, {Role: "user", Content: "original task", Resources: []resources.Ref{putResourceFixture(t, []byte("image bytes"), "image/png")}}}, opaqueCompactionGroup("last-call")...)
	messages = append(messages, provider.ChatMessage{Role: "system", Content: "dynamic task report", RuntimeData: true})
	cp, err := saveContextCheckpoint(ctx, command, messages)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistCheckpoint(db, ctx, command, cp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRun(ctx, "session", "run", "answer", "succeeded", "done"); err != nil {
		t.Fatal(err)
	}
	next, _, err := db.BeginRun(ctx, "session", "next", "next-user", "continue")
	if err != nil {
		t.Fatal(err)
	}
	command.RunID, command.MessageSequence, command.Prompt = "next", next.Sequence, "continue"
	loaded, err := loadConversationContext(ctx, command, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := append(messages[1:], provider.ChatMessage{Role: "assistant", Content: "done", Resources: []resources.Ref{}})
	if !reflect.DeepEqual(loaded, want) {
		t.Fatalf("checkpoint/history mismatch: %#v", loaded)
	}
	command.Model.ProviderID = "other-relay"
	changed, err := loadConversationContext(ctx, command, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range changed {
		if len(m.ResponseOutput) > 0 || len(m.AnthropicBlocks) > 0 || len(m.BedrockParts) > 0 {
			t.Fatal("replayed signed state to another provider")
		}
		for _, call := range m.ToolCalls {
			if call.ThoughtSignature != "" || call.GeminiID != "" {
				t.Fatal("replayed signed tool state")
			}
		}
	}
	if len(changed[1].Resources) != 1 || changed[2].ToolCalls[0].ID != "last-call" || changed[3].ToolCallID != "last-call" {
		t.Fatal("lost portable images or tool pairs")
	}
	if !changed[len(changed)-2].RuntimeData {
		t.Fatal("model switch turned a runtime report into permanent system rules")
	}
	command.Model.Provider = "bedrock-mantle-chat"
	changed, err = loadConversationContext(ctx, command, 0)
	if err != nil || len(changed[1].Resources) != 1 || len(changed[1].Images) != 0 {
		t.Fatalf("text-only protocol lost the resource reference: %+v %v", changed, err)
	}
	request, err := materializeResources(ctx, command, changed)
	if err != nil || len(request[1].Images) != 0 || !strings.Contains(request[1].Content, "omitted from this request") || changed[1].Content != "original task" {
		t.Fatalf("text-only request mutated canonical context: %+v %v", request, err)
	}
}

func TestManualCompactionLoadsAllHistoryPersistsAndLeavesMessagesUntouched(t *testing.T) {
	db, ctx := checkpointFixture(t)
	for i := 0; i < 55; i++ {
		run := fmt.Sprintf("run-%d", i)
		if _, _, err := db.BeginRun(ctx, "session", run, run+"-u", fmt.Sprintf("request %d: %s", i, strings.Repeat("original fact ", 100))); err != nil {
			t.Fatal(err)
		}
		if _, err := db.FinishRun(ctx, "session", run, run+"-a", "succeeded", "confirmed"); err != nil {
			t.Fatal(err)
		}
	}
	before, err := db.MessagesBefore(ctx, "session", 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	through, _, err := db.BeginCompaction(ctx, "session", "compact")
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		compactionResponse(w, []any{compactionText("The prior requests and findings were retained.")})
	}))
	defer upstream.Close()
	command := workerproto.Command{Action: "compact", SessionID: "session", RunID: "compact", MessageSequence: through, Model: modelcfg.Model{Provider: "openai-responses", ID: "model", BaseURL: upstream.URL, APIKey: "fixture"}}
	loaded, err := loadConversationContext(ctx, command, 0)
	if err != nil || len(loaded) != 110 {
		t.Fatalf("history truncated: messages=%d err=%v", len(loaded), err)
	}
	beforeTokens, err := EstimateSessionContext(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	var compactedTokens int64
	err = CompactSession(ctx, command, func(r workerproto.Result) error {
		if r.Kind == "context_notice" {
			compactedTokens = r.ContextUsage.Tokens
			return persistCheckpoint(db, ctx, command, r.Checkpoint)
		}
		if r.Kind == "tool" || r.Kind == "model_complete" {
			t.Fatalf("maintenance emitted %s", r.Kind)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	afterTokens, err := EstimateSessionContext(ctx, command)
	if err != nil || afterTokens <= 0 || afterTokens >= beforeTokens || afterTokens != compactedTokens {
		t.Fatalf("retained context estimate before=%d after=%d reported=%d error=%v", beforeTokens, afterTokens, compactedTokens, err)
	}
	if _, err := db.FinishCompaction(ctx, "session", "compact", "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	after, err := db.MessagesBefore(ctx, "session", 0, 200)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("visible history changed: %v", err)
	}
	status, err := db.CompactionStatus(ctx, "session")
	if err != nil || status.Phase != "compacted" || !status.Manual {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	next, _, err := db.BeginRun(ctx, "session", "next", "next-user", "continue")
	if err != nil {
		t.Fatal(err)
	}
	command.Action, command.MessageSequence = "", next.Sequence
	loaded, err = loadConversationContext(ctx, command, 0)
	if err != nil || len(loaded) >= 110 || !strings.HasPrefix(loaded[0].Content, executionSummaryPrefix) {
		t.Fatalf("compressed context not reused: %d %v", len(loaded), err)
	}
}

func TestManualCompactionOfSingleReplyPersistsAndResumesUnlessAwaitingUser(t *testing.T) {
	for _, awaiting := range []bool{false, true} {
		t.Run(fmt.Sprintf("awaiting-user=%v", awaiting), func(t *testing.T) {
			db, ctx := checkpointFixture(t)
			const prompt = "Keep the verified facts and the original constraints."
			const summary = "Retained verified finding: SOURCE_END."
			full := strings.Repeat("Verified finding and supporting evidence. ", 160) + "SOURCE_END"
			if _, _, err := db.BeginRun(ctx, "session", "original", "u", prompt); err != nil {
				t.Fatal(err)
			}
			var pending *store.PendingReply
			if awaiting {
				questions := []question.Question{{Question: full, Options: []question.Option{{Label: "Continue"}}}}
				full = question.Format(questions)
				pending = &store.PendingReply{CallID: "ask", Kind: "question", Content: full, Questions: questions}
			}
			if _, err := db.FinishRunWithPending(ctx, "session", "original", "a", "succeeded", full, pending); err != nil {
				t.Fatal(err)
			}
			beforeMessages, err := db.MessagesBefore(ctx, "session", 0, 10)
			if err != nil || len(beforeMessages) != 2 {
				t.Fatalf("single-turn fixture: %v %v", beforeMessages, err)
			}
			through, _, err := db.BeginCompaction(ctx, "session", "compact")
			if err != nil {
				t.Fatal(err)
			}
			var summaryRequests atomic.Int32
			var sourceSeen, resumed atomic.Bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Instructions string          `json:"instructions"`
					Input        json.RawMessage `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if strings.HasPrefix(body.Instructions, executionSummaryPrompt) {
					summaryRequests.Add(1)
					if strings.Contains(string(body.Input), "SOURCE_END") {
						sourceSeen.Store(true)
					}
					compactionResponse(w, []any{compactionText(summary)})
					return
				}
				input := string(body.Input)
				if !strings.Contains(input, "Summary of completed execution") || !strings.Contains(input, summary) || !strings.Contains(input, prompt) || strings.Contains(input, full) {
					t.Error("next request did not use the summary with the original user input")
				}
				resumed.Store(true)
				compactionResponse(w, []any{compactionText("Continued with the saved summary.")})
			}))
			defer upstream.Close()
			command := workerproto.Command{Action: "compact", SessionID: "session", RunID: "compact", MessageSequence: through, Model: modelcfg.Model{Provider: "openai-responses", ID: "fixture", BaseURL: upstream.URL, APIKey: "fixture"}}
			before, err := EstimateSessionContext(ctx, command)
			if err != nil {
				t.Fatal(err)
			}
			var notice *workerproto.Result
			err = CompactSession(ctx, command, func(result workerproto.Result) error {
				if result.Kind == "context_notice" {
					notice = &result
					return persistCheckpoint(db, ctx, command, result.Checkpoint)
				}
				return nil
			})
			if awaiting {
				if err == nil || notice != nil || summaryRequests.Load() != 0 {
					t.Fatalf("pending reply was compacted: notice=%v requests=%d error=%v", notice, summaryRequests.Load(), err)
				}
				if _, err := db.FinishCompaction(ctx, "session", "compact", "failed", "No completed reply to compact"); err != nil {
					t.Fatal(err)
				}
				current, err := db.Get(ctx, "session")
				if err != nil || current.Pending == nil || current.Pending.Content != full {
					t.Fatalf("pending question changed: %+v %v", current.Pending, err)
				}
				return
			}
			if err != nil || notice == nil || notice.Checkpoint == nil || !sourceSeen.Load() {
				t.Fatalf("single reply was not compacted: notice=%v sourceSeen=%v error=%v", notice, sourceSeen.Load(), err)
			}
			if _, err := db.FinishCompaction(ctx, "session", "compact", "succeeded", ""); err != nil {
				t.Fatal(err)
			}
			after, err := EstimateSessionContext(ctx, command)
			if err != nil || after >= before || after != notice.ContextUsage.Tokens {
				t.Fatalf("compaction did not reduce persisted context: before=%d after=%d error=%v", before, after, err)
			}
			afterMessages, err := db.MessagesBefore(ctx, "session", 0, 10)
			if err != nil || !reflect.DeepEqual(beforeMessages, afterMessages) {
				t.Fatalf("compaction changed the visible conversation: %v", err)
			}
			next, _, err := db.BeginRun(ctx, "session", "next", "continue", "Continue")
			if err != nil {
				t.Fatal(err)
			}
			command.Action, command.RunID, command.MessageSequence, command.Prompt = "run", "next", next.Sequence, "Continue"
			answer, err := Run(ctx, command, func(workerproto.Result) error { return nil })
			if err != nil || !resumed.Load() || answer != "Continued with the saved summary." {
				t.Fatalf("did not resume from compressed context: answer=%q error=%v", answer, err)
			}
		})
	}
}

func TestSummaryOverflowShrinksBatchAndFailedCommitKeepsPriorCheckpoint(t *testing.T) {
	db, ctx := checkpointFixture(t)
	u, _, err := db.BeginRun(ctx, "session", "run", "u", "task")
	if err != nil {
		t.Fatal(err)
	}
	requests, overflows := 0, 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.ContentLength > 1700 {
			overflows++
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"message":"prompt is too long"}}`)
			return
		}
		compactionResponse(w, []any{compactionText("saved facts")})
	}))
	defer upstream.Close()
	command := workerproto.Command{SessionID: "session", RunID: "run", MessageSequence: u.Sequence, Prompt: "task", Model: modelcfg.Model{Provider: "openai-responses", ID: "model", BaseURL: upstream.URL, APIKey: "fixture"}}
	messages := []provider.ChatMessage{{Role: "system", Content: "instructions"}, {Role: "user", Content: "task"}, {Role: "assistant", Content: strings.Repeat("facts ", 800)}, {Role: "assistant", Content: strings.Repeat("retained ", 400)}}
	c := newExecutionCompactor(messages)
	_, changed, err := c.compact(ctx, command, messages, nil, func(r workerproto.Result) error {
		if r.Kind == "context_notice" {
			return fmt.Errorf("commit rejected")
		}
		return nil
	}, &runScope{}, 0)
	if err == nil || changed || overflows == 0 || requests > 32 {
		t.Fatalf("changed=%v requests=%d overflows=%d err=%v", changed, requests, overflows, err)
	}
	cp, err := db.ContextCheckpoint(ctx, "session")
	if err != nil || cp != nil || c.summary != "" {
		t.Fatalf("failed compaction committed: %+v %v", cp, err)
	}
}
