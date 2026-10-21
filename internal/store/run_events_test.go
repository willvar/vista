package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"vista/internal/workerproto"
)

func TestRunEventRecoversOriginalToolBodyAndKeepsRunIsolation(t *testing.T) {
	st, ctx := executionStore(t)
	body := strings.Repeat("原始工具输出 ", 1200)
	original, err := st.AppendRunEvent(ctx, "session", "run", "tool", workerproto.Result{RunID: "run", Kind: "tool", Turn: 300, TaskID: "main/child", ParentTaskID: "main", Status: "succeeded", Content: body, Tool: &workerproto.ToolProgress{CallID: "call-original", Name: "shell"}})
	if err != nil {
		t.Fatal(err)
	}
	overview, err := st.RunOverview(ctx, "session", "run")
	if err != nil {
		t.Fatal(err)
	}
	var compact workerproto.Result
	for _, event := range overview.Events {
		if event.Sequence == original.Sequence {
			if err := json.Unmarshal(event.Data, &compact); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(compact.Content) > 512 || !compact.Tool.Truncated {
		t.Fatalf("overview is not compact: %+v", compact.Tool)
	}
	recovered, err := st.RunEvent(ctx, "session", "run", original.Sequence)
	if err != nil || !bytes.Equal(recovered.Data, original.Data) || recovered.CreatedAt != original.CreatedAt || recovered.Sequence != original.Sequence || recovered.Kind != "tool" {
		t.Fatalf("original event changed: %+v %v", recovered, err)
	}
	var decoded workerproto.Result
	if err := json.Unmarshal(recovered.Data, &decoded); err != nil || decoded.Content != body || decoded.Tool.Truncated || decoded.TaskID != "main/child" {
		t.Fatalf("tool recovery lost text or metadata: %+v %v", decoded.Tool, err)
	}
	if _, err := st.Create(ctx, "other-session", "private"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.BeginRun(ctx, "other-session", "other-run", "other-input", "private"); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []struct{ session, run string }{{"other-session", "run"}, {"session", "other-run"}, {"session", "missing-run"}} {
		if _, err := st.RunEvent(ctx, scope.session, scope.run, original.Sequence); !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign event read %+v: %v", scope, err)
		}
	}
	if _, err := st.RunEvent(ctx, "session", "run", 999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing event read: %v", err)
	}
}

func TestRunTurnEventsPageChronologicallyAndExcludeOtherTurnsAndRuns(t *testing.T) {
	st, ctx := executionStore(t)
	const turn = int32(300)
	var originals []Event
	for index := 0; index < 125; index++ {
		kind := []string{"model_request", "model_reasoning_delta", "model_delta", "tool", "model_result"}[index%5]
		data := workerproto.Result{RunID: "run", Kind: kind, Turn: turn, Depth: 1, TaskID: "main/child", ParentTaskID: "main"}
		switch kind {
		case "model_request":
			data.ModelUse = &workerproto.ModelUse{Provider: "openai-chat", ID: "test"}
		case "model_result":
			data.ModelResult = &workerproto.ModelResult{Provider: "openai-chat", ID: "test", FinishReason: "tool_calls"}
		case "tool":
			data.Status = "succeeded"
			data.Content = fmt.Sprintf("output %d", index)
			data.Tool = &workerproto.ToolProgress{CallID: fmt.Sprintf("call-%d", index), Name: "shell"}
		default:
			data.Content = fmt.Sprintf("fragment %d", index)
		}
		event, err := st.AppendRunEvent(ctx, "session", "run", kind, data)
		if err != nil {
			t.Fatal(err)
		}
		originals = append(originals, *event)
		if index%5 == 0 {
			if _, err := st.AppendRunEvent(ctx, "session", "run", "model_delta", workerproto.Result{RunID: "run", Kind: "model_delta", Turn: turn + 1, Content: "other turn"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := st.FinishRun(ctx, "session", "run", "answer", "succeeded", "done"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.BeginRun(ctx, "session", "next-run", "next-input", "next"); err != nil {
		t.Fatal(err)
	}
	foreign, err := st.AppendRunEvent(ctx, "session", "next-run", "model_delta", workerproto.Result{RunID: "next-run", Kind: "model_delta", Turn: turn, Content: "another run"})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := st.RunTurnEvents(ctx, "session", "run", turn, 0)
	if err != nil || len(latest) != 100 {
		t.Fatalf("latest page: count=%d err=%v", len(latest), err)
	}
	for index, event := range latest {
		want := originals[index+25]
		if event.Sequence != want.Sequence || !bytes.Equal(event.Data, want.Data) || event.CreatedAt != want.CreatedAt {
			t.Fatalf("latest page order/filter index %d: %+v want %+v", index, event, want)
		}
	}
	older, err := st.RunTurnEvents(ctx, "session", "run", turn, latest[0].Sequence)
	if err != nil || len(older) != 25 {
		t.Fatalf("older page: count=%d err=%v", len(older), err)
	}
	for index, event := range older {
		if event.Sequence != originals[index].Sequence {
			t.Fatalf("older page order index %d: %+v", index, event)
		}
	}
	end, err := st.RunTurnEvents(ctx, "session", "run", turn, older[0].Sequence)
	if err != nil || len(end) != 0 {
		t.Fatalf("end page: %+v %v", end, err)
	}
	empty, err := st.RunTurnEvents(ctx, "session", "run", 2147483647, 0)
	if err != nil || len(empty) != 0 {
		t.Fatalf("large valid turn: %+v %v", empty, err)
	}
	if _, err := st.RunEvent(ctx, "session", "run", foreign.Sequence); !errors.Is(err, ErrNotFound) {
		t.Fatalf("same-session foreign-run event exposed: %v", err)
	}
	if _, err := st.RunTurnEvents(ctx, "foreign-session", "run", turn, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign run page exposed: %v", err)
	}
	if _, err := st.RunTurnEvents(ctx, "session", "missing-run", turn, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing run accepted: %v", err)
	}
}

func TestRunEventsValidatePositiveRanges(t *testing.T) {
	st, ctx := executionStore(t)
	for _, sequence := range []int64{0, -1} {
		if _, err := st.RunEvent(ctx, "session", "run", sequence); !errors.Is(err, ErrRunEventRange) {
			t.Fatalf("sequence %d: %v", sequence, err)
		}
	}
	for _, turn := range []int32{0, -1} {
		if _, err := st.RunTurnEvents(ctx, "session", "run", turn, 0); !errors.Is(err, ErrRunEventRange) {
			t.Fatalf("turn %d: %v", turn, err)
		}
	}
	if _, err := st.RunTurnEvents(ctx, "session", "run", 300, -1); !errors.Is(err, ErrRunEventRange) {
		t.Fatalf("negative before: %v", err)
	}
}
