package server

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"vista/internal/workerproto"
)

func TestToolProgressAcceptsSuccessAboveLegacyTwoKiBLimit(t *testing.T) {
	for _, content := range []string{strings.Repeat("x", 7000), strings.Repeat("\x00", 8192), strings.Repeat("测", 4000)} {
		result := workerproto.Result{RunID: "run", Kind: "tool", TaskID: "child", ParentTaskID: "main", Status: "succeeded", Content: content, Tool: &workerproto.ToolProgress{CallID: "call", Name: "shell", OutputRef: &workerproto.OutputRef{RunID: "run", Turn: 7, Bytes: 12000}}}
		record, valid := prepareToolProgress(t.TempDir(), result)
		if !valid {
			t.Fatal("valid success preview was rejected")
		}
		encoded, err := json.Marshal(record)
		if err != nil || len(encoded) > maxToolEventBytes || len(record.Content) > maxToolPreviewBytes || !utf8.ValidString(record.Content) {
			t.Fatalf("invalid persisted event: bytes=%d content=%d err=%v", len(encoded), len(record.Content), err)
		}
		if record.Tool.OutputRef == nil || record.Tool.OutputRef.Turn != 7 || record.TaskID != "child" {
			t.Fatal("tool metadata discarded")
		}
		if len(record.Content) < len(content) && !record.Tool.Truncated {
			t.Fatal("truncated preview not marked")
		}
		if result.Tool.Truncated {
			t.Fatal("normalization mutated incoming pointer")
		}
		if strings.HasPrefix(content, "x") && record.Content != content {
			t.Fatal("valid 8–24 KiB tool result lost text")
		}
	}
}

func TestTaskProgressAllowsCanceledAndVerifiesReportIdentity(t *testing.T) {
	result := workerproto.Result{RunID: "run", TaskID: "child", ParentTaskID: "main", Task: &workerproto.AgentTask{ID: "child", ParentID: "main", Title: "Check files", Status: "canceled"}}
	if !validAgentTaskProgress(t.TempDir(), result) {
		t.Fatal("canceled task rejected")
	}
	result.Task.Status = "succeeded"
	result.Task.Report = &workerproto.OutputRef{RunID: "different-run", Turn: 1, Bytes: 10}
	if validAgentTaskProgress(t.TempDir(), result) {
		t.Fatal("cross-run report accepted")
	}
	result.Task.Report.RunID = "run"
	if !validAgentTaskProgress(t.TempDir(), result) {
		t.Fatal("valid report rejected")
	}
}
