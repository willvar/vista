package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestAgentEmptyReplyRequiresNormalProtocolCompletion(t *testing.T) {
	for _, test := range []struct {
		name, protocol, stream string
		status                 int
		wantError              bool
	}{
		{"Responses empty final", "openai-responses", `data: {"type":"response.completed","response":{"status":"completed","output":[{"id":"empty","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":""}]}],"usage":{"input_tokens":100,"output_tokens":4}}}` + "\n\n", 200, false},
		{"Responses whitespace", "openai-responses", `data: {"type":"response.completed","response":{"status":"completed","output":[{"id":"empty","type":"message","role":"assistant","content":[{"type":"output_text","text":" \n"}]}]}}` + "\n\n", 200, false},
		{"Chat empty stop", "openai-chat", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", 200, false},
		{"Anthropic empty end turn", "anthropic", "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n", 200, false},
		{"Responses interrupted", "openai-responses", "data: {\"type\":\"response.created\"}\n\n", 200, true},
		{"Responses failed", "openai-responses", "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"server_error\",\"message\":\"fixture failure\"}}}\n\n", 200, true},
		{"Responses incomplete", "openai-responses", "data: {\"type\":\"response.incomplete\",\"response\":{\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n", 200, true},
		{"Chat missing stop", "openai-chat", "data: [DONE]\n\n", 200, true},
		{"HTTP rejection", "openai-responses", `{"error":{"message":"fixture rejection"}}`, 403, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.stream)
			}))
			defer upstream.Close()
			command := workerproto.Command{RunID: "run", Prompt: "Complete the task.", Model: modelcfg.Model{Provider: test.protocol, ID: "fixture", BaseURL: upstream.URL, APIKey: "test"}}
			var completed []workerproto.Result
			answer, err := Run(context.Background(), command, func(result workerproto.Result) error {
				if result.Kind == "model_complete" {
					completed = append(completed, result)
				}
				return nil
			})
			if (err != nil) != test.wantError || answer != "" || calls.Load() != 1 {
				t.Fatalf("answer=%q err=%v requests=%d", answer, err, calls.Load())
			}
			if test.wantError {
				if len(completed) != 0 {
					t.Fatal("failed response emitted a normal completion")
				}
			} else if len(completed) != 1 || completed[0].Turn != 0 || completed[0].FullBytes != 0 {
				t.Fatalf("empty completion invented a text output reference: %+v", completed)
			}
		})
	}
}

func TestEmptyReplyPreservesFileDeliveryAndCrossRunHistory(t *testing.T) {
	db, ctx := checkpointFixture(t)
	path := filepath.Join(t.TempDir(), "answer.txt")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Input []map[string]any }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		switch calls.Add(1) {
		case 1:
			args, _ := json.Marshal(map[string]string{"path": path, "content": "4", "expectedSha256": "missing"})
			compactionResponse(w, []any{map[string]any{"type": "reasoning", "id": "rs_tool", "summary": []any{}, "encrypted_content": "tool-state"}, map[string]any{"type": "function_call", "id": "fc_write", "call_id": "write", "name": "write_file", "arguments": string(args)}})
		case 2:
			last := body.Input[len(body.Input)-1]
			if last["type"] != "function_call_output" || last["call_id"] != "write" || !strings.Contains(fmt.Sprint(last["output"]), `"sha256"`) {
				t.Errorf("file result was not delivered: %+v", last)
			}
			item := compactionText("")
			item["phase"] = "final_answer"
			compactionResponse(w, []any{item})
		case 3:
			writes, results, states := 0, 0, 0
			for _, item := range body.Input {
				if item["type"] == "function_call" && item["name"] == "write_file" {
					writes++
				}
				if item["type"] == "function_call_output" && item["call_id"] == "write" {
					results++
				}
				if item["type"] == "reasoning" && item["encrypted_content"] == "tool-state" {
					states++
				}
			}
			if writes != 1 || results != 1 || states != 1 {
				t.Errorf("follow-up lost/duplicated completed evidence: writes=%d results=%d states=%d", writes, results, states)
			}
			compactionResponse(w, []any{compactionText("The saved answer is 4.")})
		default:
			t.Error("empty completion retried the model or repeated tools")
		}
	}))
	defer upstream.Close()
	user, _, err := db.BeginRun(ctx, "session", "first", "user", "Write the answer to the file.")
	if err != nil {
		t.Fatal(err)
	}
	command := workerproto.Command{SessionID: "session", RunID: "first", MessageSequence: user.Sequence, Prompt: user.Content,
		Model: modelcfg.Model{Provider: "openai-responses", ID: "fixture", BaseURL: upstream.URL, APIKey: "test"}}
	var final store.ModelOutputRef
	answer, err := Run(ctx, command, historyTestSink(db, command, &final))
	if err != nil || answer != "" || calls.Load() != 2 || final != (store.ModelOutputRef{}) {
		t.Fatalf("file delivery ended incorrectly: answer=%q err=%v calls=%d output=%+v", answer, err, calls.Load(), final)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "4" {
		t.Fatalf("answer file=%q err=%v", data, err)
	}
	finished, err := db.FinishRun(ctx, "session", "first", "reply", "succeeded", answer)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Status     string
		EmptyReply bool
		Message    *store.Message
	}
	if err := json.Unmarshal(finished.Data, &result); err != nil || result.Status != "succeeded" || !result.EmptyReply || result.Message != nil {
		t.Fatalf("empty completion created a reply or failure: %+v err=%v", result, err)
	}
	user, _, err = db.BeginRun(ctx, "session", "second", "followup", "What was saved?")
	if err != nil {
		t.Fatal(err)
	}
	command.RunID, command.MessageSequence, command.Prompt = "second", user.Sequence, user.Content
	answer, err = Run(ctx, command, historyTestSink(db, command, &final))
	if err != nil || answer != "The saved answer is 4." || calls.Load() != 3 {
		t.Fatalf("follow-up failed: answer=%q err=%v calls=%d", answer, err, calls.Load())
	}
}

func TestEmptyReplyConsumesPendingInputWithoutInventingReply(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var calls atomic.Int32
	var pending atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Messages []provider.ChatMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if calls.Add(1) == 1 {
			pending.Store(true)
			continuationAnswer(w, "")
			return
		}
		last := body.Messages[len(body.Messages)-1]
		if last.Role != "user" || last.Content != "Follow up." {
			t.Errorf("pending input was lost: %+v", last)
		}
		continuationAnswer(w, "Follow-up handled.")
	}))
	defer upstream.Close()
	delivered := false
	command := continuationCommand(upstream.URL, "First task.")
	answer, err := RunWithInputSource(context.Background(), command, func(workerproto.Result) error { return nil }, func(reply *workerproto.CompletedReply) (*workerproto.Command, error) {
		if !pending.Swap(false) {
			return nil, nil
		}
		if reply != nil {
			t.Errorf("empty completion invented a message: %+v", reply)
		}
		delivered = true
		return &workerproto.Command{RunID: command.RunID, InputID: "followup", Prompt: "Follow up."}, nil
	}, nil)
	if err != nil || answer != "Follow-up handled." || !delivered || calls.Load() != 2 {
		t.Fatalf("pending input after empty reply: answer=%q err=%v delivered=%v calls=%d", answer, err, delivered, calls.Load())
	}
}

func TestEmptyReplyLeavesFollowupProtocolHistoryUsable(t *testing.T) {
	db, ctx := checkpointFixture(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string
				Content json.RawMessage
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		for _, message := range body.Messages {
			if message.Role == "assistant" && (string(message.Content) == "[]" || string(message.Content) == `""`) {
				http.Error(w, "messages must have non-empty content", http.StatusBadRequest)
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) == 2 {
			fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"Follow-up handled.\"}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		}
		fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer upstream.Close()
	command := workerproto.Command{SessionID: "session", Model: modelcfg.Model{Provider: "anthropic", ID: "fixture", BaseURL: upstream.URL, APIKey: "test"}}
	for i, prompt := range []string{"Complete the task.", "Follow up."} {
		user, _, err := db.BeginRun(ctx, "session", fmt.Sprint(i), fmt.Sprint("user", i), prompt)
		if err != nil {
			t.Fatal(err)
		}
		command.RunID, command.MessageSequence, command.Prompt = user.RunID, user.Sequence, prompt
		var output store.ModelOutputRef
		answer, err := Run(ctx, command, historyTestSink(db, command, &output))
		want := ""
		if i == 1 {
			want = "Follow-up handled."
		}
		if err != nil || answer != want {
			t.Fatalf("run %d: answer=%q err=%v", i, answer, err)
		}
		if _, err := db.FinishRun(ctx, "session", user.RunID, fmt.Sprint("reply", i), "succeeded", answer); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("empty completion retried: calls=%d", calls.Load())
	}
}

func TestEmptyReplyStillJoinsRequiredChild(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var parentTurns atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Messages []provider.ChatMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.Messages[1].Content == "child" {
			continuationAnswer(w, "")
			return
		}
		switch parentTurns.Add(1) {
		case 1:
			continuationEvent(w, mcpToolEvent("child", "delegate", `{"task":"child"}`))
		case 2:
			continuationAnswer(w, "")
		case 3:
			last := body.Messages[len(body.Messages)-1]
			if last.Role != "system" || !strings.Contains(last.Content, `"type":"runtime_join"`) || !strings.Contains(last.Content, `"status":"succeeded"`) {
				t.Errorf("empty reply bypassed required-child collection: %+v", last)
			}
			continuationAnswer(w, "Child collected.")
		default:
			t.Error("unexpected model retry")
		}
	}))
	defer upstream.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	answer, err := Run(ctx, continuationCommand(upstream.URL, "main"), func(workerproto.Result) error { return nil })
	if err != nil || answer != "Child collected." || parentTurns.Load() != 3 {
		t.Fatalf("required join: answer=%q err=%v turns=%d", answer, err, parentTurns.Load())
	}
}
