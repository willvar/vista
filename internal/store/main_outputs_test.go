package store

import (
	"errors"
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
