package store

import (
	"encoding/json"
	"fmt"
	"testing"

	"vista/internal/agentconfig"
	"vista/internal/workerproto"
)

func TestNestedModelRecordsAcceptSupportedDepths(t *testing.T) {
	st, ctx := executionStore(t)
	for _, depth := range []int{3, agentconfig.MaxDelegationDepth} {
		request := workerproto.Result{RunID: "run", Turn: int32(depth), Depth: depth, TaskID: fmt.Sprintf("child-%d", depth), ParentTaskID: "parent", ModelUse: &workerproto.ModelUse{Provider: "openai-chat", ID: "model"}}
		requestEvent, err := st.RecordModelRequestEvent(ctx, "session", request)
		if err != nil || requestEvent == nil {
			t.Fatalf("request depth %d: event=%+v error=%v", depth, requestEvent, err)
		}
		fragmentEvent, err := st.AppendModelDelta(ctx, "session", "run", int32(depth), depth, "nested text", request.TaskID, request.ParentTaskID)
		if err != nil || fragmentEvent == nil {
			t.Fatalf("fragment depth %d: event=%+v error=%v", depth, fragmentEvent, err)
		}
		for _, event := range []*Event{requestEvent, fragmentEvent} {
			var saved workerproto.Result
			if err := json.Unmarshal(event.Data, &saved); err != nil || saved.Depth != depth || saved.TaskID != request.TaskID || saved.ParentTaskID != request.ParentTaskID {
				t.Fatalf("saved nested identity: %+v error=%v", saved, err)
			}
		}
		if page, err := st.ReadFullModelOutput(ctx, "session", "run", int32(depth)); err != nil || page.Content != "nested text" {
			t.Fatalf("read depth %d output: %+v error=%v", depth, page, err)
		}
	}
	for _, depth := range []int{-1, agentconfig.MaxDelegationDepth + 1} {
		request := workerproto.Result{RunID: "run", Turn: 10, Depth: depth, ModelUse: &workerproto.ModelUse{Provider: "openai-chat", ID: "model"}}
		if _, err := st.RecordModelRequestEvent(ctx, "session", request); err == nil {
			t.Fatalf("invalid request depth %d accepted", depth)
		}
		if _, err := st.AppendModelDelta(ctx, "session", "run", 10, depth, "invalid text"); err == nil {
			t.Fatalf("invalid fragment depth %d accepted", depth)
		}
	}
}
