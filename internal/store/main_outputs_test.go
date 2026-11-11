package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"vista/internal/workerproto"
)

func TestMainOutputsIndexPagesAllCallsWithoutChangingMessages(t *testing.T) {
	s, ctx := executionStore(t)
	const calls = 107
	var first, last int64
	for turn := int32(1); turn <= calls; turn++ {
		if _, err := s.RecordModelRequestEvent(ctx, "session", workerproto.Result{RunID: "run", Turn: turn, ModelUse: &workerproto.ModelUse{Provider: "openai-chat", ID: "model", Key: "choice"}}); err != nil {
			t.Fatal(err)
		}
		parts := 1
		if turn == 1 {
			parts = 10
		}
		for range parts {
			event, err := s.AppendModelDelta(ctx, "session", "run", turn, 0, strings.Repeat("正文", 250))
			if err != nil {
				t.Fatal(err)
			}
			if first == 0 {
				first = event.Sequence
			}
			if turn == 1 {
				last = event.Sequence
			}
		}
		if turn < calls {
			if _, err := s.AppendRunEvent(ctx, "session", "run", "model_result", workerproto.Result{RunID: "run", Turn: turn, ModelResult: &workerproto.ModelResult{Provider: "openai-chat", ID: "model", FinishReason: "tool_calls"}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.AppendModelDelta(ctx, "session", "run", 108, 1, "child text", "child"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordModelRequestEvent(ctx, "session", workerproto.Result{RunID: "run", Turn: 109, ModelUse: &workerproto.ModelUse{Provider: "openai-chat", ID: "model"}}); err != nil {
		t.Fatal(err)
	}
	page, err := s.MainOutputs(ctx, "session", "run", 0)
	if err != nil || len(page) != 100 || page[0].Turn != 8 || page[99].Turn != 107 || page[99].Complete {
		t.Fatalf("latest page: %d %v", len(page), err)
	}
	older, err := s.MainOutputs(ctx, "session", "run", page[0].Sequence)
	if err != nil || len(older) != 7 {
		t.Fatalf("older page: %d %v", len(older), err)
	}
	output := older[0]
	if output.Sequence != first || output.LastSequence != last || output.Bytes != 15000 || !output.Complete || output.Provider != "openai-chat" || output.ModelID != "model" || output.ModelKey != "choice" || output.TaskID != "main" || output.CreatedAt == "" {
		t.Fatalf("lost output metadata: %+v", output)
	}
	full, err := s.ReadFullModelOutput(ctx, "session", "run", 1)
	if err != nil || len(full.Content) != 15000 || full.LastSequence != last || full.NextOffset != full.AvailableBytes {
		t.Fatalf("full body: %+v %v", full, err)
	}
	messages, err := s.MessagesBefore(ctx, "session", 0, 100)
	if err != nil || len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("index changed model history: %+v %v", messages, err)
	}
	for _, scope := range [][2]string{{"other", "run"}, {"session", "other"}} {
		if _, err := s.MainOutputs(ctx, scope[0], scope[1], 0); !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign run read: %v", err)
		}
	}
	if _, err := s.MainOutputs(ctx, "session", "run", -1); !errors.Is(err, ErrRunEventRange) {
		t.Fatalf("negative cursor: %v", err)
	}
}

func TestMainOutputsRetainInterruptedText(t *testing.T) {
	for _, status := range []string{"interrupted", "failed", "succeeded"} {
		t.Run(status, func(t *testing.T) {
			s, ctx := executionStore(t)
			if _, err := s.AppendModelDelta(ctx, "session", "run", 1, 0, "saved text", "main"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.FinishRun(ctx, "session", "run", "reply", status, ""); err != nil {
				t.Fatal(err)
			}
			page, err := s.MainOutputs(ctx, "session", "run", 0)
			if err != nil || len(page) != 1 || !page[0].Complete || page[0].Bytes != 10 {
				t.Fatalf("interrupted output: %+v %v", page, err)
			}
		})
	}
}

func TestMainOutputsExcludeReasoningFromMessages(t *testing.T) {
	s, ctx := executionStore(t)
	const calls = 55
	for turn := int32(1); turn <= calls; turn++ {
		if _, err := s.RecordModelRequestEvent(ctx, "session", workerproto.Result{RunID: "run", Turn: turn, ModelUse: &workerproto.ModelUse{Provider: "openai-responses", ID: "model", Key: "choice"}}); err != nil {
			t.Fatal(err)
		}
		parts := 1
		if turn == 1 {
			parts = 12
		}
		for range parts {
			if _, err := s.AppendRunEvent(ctx, "session", "run", "model_reasoning_delta", workerproto.Result{RunID: "run", TaskID: "main", Turn: turn, Content: strings.Repeat("思考🙂", 200)}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.AppendModelDelta(ctx, "session", "run", turn, 0, "Body", "main"); err != nil {
			t.Fatal(err)
		}
		if turn < calls {
			if _, err := s.AppendRunEvent(ctx, "session", "run", "model_result", workerproto.Result{RunID: "run", Turn: turn, ModelResult: &workerproto.ModelResult{Provider: "openai-responses", ID: "model"}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, child := range []workerproto.Result{
		{RunID: "run", Turn: 100, Depth: 1, TaskID: "child", Content: "Child reasoning"},
		{RunID: "run", Turn: 101, TaskID: "child", Content: "Child without depth"},
		{RunID: "run", Turn: 102, TaskID: "main", Content: ""},
	} {
		if _, err := s.AppendRunEvent(ctx, "session", "run", "model_reasoning_delta", child); err != nil {
			t.Fatal(err)
		}
	}
	outputs, err := s.MainOutputs(ctx, "session", "run", 0)
	if err != nil || len(outputs) != calls {
		t.Fatalf("text index: %d %v", len(outputs), err)
	}
	for i, output := range outputs {
		if output.Turn != int32(i+1) || output.Bytes != 4 || output.ModelKey != "choice" || output.Complete != (i < calls-1) {
			t.Fatalf("text metadata: %+v", output)
		}
	}
	if _, err := s.FinishRun(ctx, "session", "run", "reply", "failed", ""); err != nil {
		t.Fatal(err)
	}
	outputs, err = s.MainOutputs(ctx, "session", "run", 0)
	if err != nil || !outputs[len(outputs)-1].Complete {
		t.Fatalf("failed run text: %+v %v", outputs, err)
	}
}

func TestMainOutputsKeepTextSpansAroundReasoning(t *testing.T) {
	s, ctx := executionStore(t)
	first, err := s.AppendModelDelta(ctx, "session", "run", 1, 0, "Opening🙂", "main")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Checking one.", "Checking two.", "Checking three."} {
		if _, err := s.AppendRunEvent(ctx, "session", "run", "model_reasoning_delta", workerproto.Result{RunID: "run", TaskID: "main", Turn: 1, Content: text}); err != nil {
			t.Fatal(err)
		}
	}
	last, err := s.AppendModelDelta(ctx, "session", "run", 1, 0, "Final answer.", "main")
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := s.MainOutputs(ctx, "session", "run", 0)
	if err != nil || len(outputs) != 2 {
		t.Fatalf("text spans: %+v %v", outputs, err)
	}
	if outputs[0].Sequence != first.Sequence || outputs[0].LastSequence != first.Sequence || outputs[1].Sequence != last.Sequence {
		t.Fatalf("text spans lost their event positions: %+v", outputs)
	}
	if outputs[0].Offset != 0 || outputs[0].EndOffset != int64(len("Opening🙂")) || outputs[1].Offset != outputs[0].EndOffset || outputs[1].EndOffset != int64(len("Opening🙂Final answer.")) {
		t.Fatalf("invalid UTF-8 byte ranges: %+v", outputs)
	}
	var delta struct {
		Offset int64 `json:"offset"`
	}
	if err := json.Unmarshal(last.Data, &delta); err != nil || delta.Offset != outputs[1].Offset {
		t.Fatalf("live text offset differs from durable index: %+v %v", delta, err)
	}
	full, err := s.ReadFullModelOutput(ctx, "session", "run", 1)
	if err != nil || full.Content != "Opening🙂Final answer." {
		t.Fatalf("canonical output changed: %+v %v", full, err)
	}
}

func TestMainOutputsPageManySpansOfOneTurn(t *testing.T) {
	s, ctx := executionStore(t)
	if _, err := s.RecordModelRequestEvent(ctx, "session", workerproto.Result{RunID: "run", Turn: 1, ModelUse: &workerproto.ModelUse{Provider: "openai-responses", ID: "model", Key: "choice"}}); err != nil {
		t.Fatal(err)
	}
	var expected []MainOutput
	var body strings.Builder
	for i := range 107 {
		text := "片段🙂" + strconv.Itoa(i)
		first, err := s.AppendModelDelta(ctx, "session", "run", 1, 0, text, "main")
		if err != nil {
			t.Fatal(err)
		}
		expected = append(expected, MainOutput{Sequence: first.Sequence, Offset: int64(body.Len()), EndOffset: int64(body.Len() + len(text))})
		body.WriteString(text)
		if _, err := s.AppendRunEvent(ctx, "session", "run", "model_reasoning_delta", workerproto.Result{RunID: "run", Turn: 1, Content: "Consider the next part."}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.FinishRun(ctx, "session", "run", "reply", "succeeded", body.String(), ModelOutputRef{Turn: 1, Bytes: body.Len()}); err != nil {
		t.Fatal(err)
	}
	latest, err := s.MainOutputs(ctx, "session", "run", 0)
	if err != nil || len(latest) != 100 {
		t.Fatalf("latest span page: %d %v", len(latest), err)
	}
	older, err := s.MainOutputs(ctx, "session", "run", latest[0].Sequence)
	if err != nil || len(older) != 7 {
		t.Fatalf("older span page: %d %v", len(older), err)
	}
	for i, output := range append(older, latest...) {
		want := expected[i]
		if output.Sequence != want.Sequence || output.LastSequence != want.Sequence || output.Offset != want.Offset || output.EndOffset != want.EndOffset || output.Bytes != int64(body.Len()) || !output.Complete || output.ModelKey != "choice" {
			t.Fatalf("span %d: %+v; expected %+v", i, output, want)
		}
	}
	full, err := s.ReadFullModelOutput(ctx, "session", "run", 1)
	if err != nil || full.Content != body.String() || !full.Complete {
		t.Fatalf("canonical body changed: %+v %v", full, err)
	}
	// This also exercises existing deltas that predate the live offset field.
	if _, err := s.db.ExecContext(ctx, "UPDATE events SET data=json_remove(data,'$.offset') WHERE session_id='session' AND kind='model_delta'"); err != nil {
		t.Fatal(err)
	}
	restored, err := s.MainOutputs(ctx, "session", "run", 0)
	if err != nil || !reflect.DeepEqual(restored, latest) {
		t.Fatalf("legacy event restoration changed spans: %v", err)
	}
}

func TestMainOutputsSplitOnlyAtVisibleContent(t *testing.T) {
	for index, boundary := range []struct {
		kind string
		data map[string]any
	}{
		{"model_reasoning_delta", map[string]any{"runId": "run", "turn": 1, "content": "Reasoning"}},
		{"model_delta", map[string]any{"runId": "run", "taskId": "child", "depth": 1, "turn": 2, "content": "Child output"}},
		{"model_reasoning_delta", map[string]any{"runId": "earlier-run", "taskId": "child", "turn": 2, "content": "Detached reasoning"}},
		{"tool", map[string]any{"runId": "run", "tool": map[string]any{"name": "shell"}}},
		{"model_retry", map[string]any{"runId": "run", "modelRetry": map[string]any{"attempt": 1}}},
		{"source", map[string]any{"runId": "run", "content": "https://example.com"}},
		{"message", map[string]any{"runId": "run", "role": "user", "content": "Follow up"}},
	} {
		t.Run(boundary.kind+"/"+strconv.Itoa(index), func(t *testing.T) {
			s, ctx := executionStore(t)
			first, err := s.AppendModelDelta(ctx, "session", "run", 1, 0, "Before", "main")
			if err != nil {
				t.Fatal(err)
			}
			for _, hidden := range []map[string]any{
				{"runId": "run", "turn": 1, "content": ""},
				{"runId": "run", "turn": 1, "purpose": "summary", "content": "Auxiliary text"},
			} {
				if _, err := s.AppendRunEvent(ctx, "session", "run", "model_reasoning_delta", hidden); err != nil {
					t.Fatal(err)
				}
			}
			continued, err := s.AppendModelDelta(ctx, "session", "run", 1, 0, " boundary", "main")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.AppendRunEvent(ctx, "session", "run", boundary.kind, boundary.data); err != nil {
				t.Fatal(err)
			}
			last, err := s.AppendModelDelta(ctx, "session", "run", 1, 0, "After", "main")
			if err != nil {
				t.Fatal(err)
			}
			outputs, err := s.MainOutputs(ctx, "session", "run", 0)
			if err != nil || len(outputs) != 2 || outputs[0].Sequence != first.Sequence || outputs[0].LastSequence != continued.Sequence || outputs[1].Sequence != last.Sequence {
				t.Fatalf("visible boundary lost: %+v %v", outputs, err)
			}
		})
	}
}
