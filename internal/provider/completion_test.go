package provider

import "testing"

func TestNormalStopRequiresExplicitSuccessfulTermination(t *testing.T) {
	for _, reason := range []string{"stop", "end", "end_turn", "stop_sequence", "completed", "COMPLETE", "STOP", "STOP_SEQUENCE"} {
		if !(Turn{FinishReason: reason}).StoppedNormally() {
			t.Errorf("normal stop %q was rejected", reason)
		}
	}
	for _, reason := range []string{"", "failed", "incomplete", "length", "MAX_TOKENS", "content_filter", "tool_calls", "tool_use", "TOOL_CALL", "unknown"} {
		if (Turn{FinishReason: reason}).StoppedNormally() {
			t.Errorf("missing or abnormal stop %q was accepted", reason)
		}
	}
	if (Turn{FinishReason: "completed", Calls: []ToolCall{{ID: "pending"}}}).StoppedNormally() {
		t.Fatal("a tool-bearing response cannot finish the execution")
	}
}
