package worker

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"vista/internal/workerproto"
)

func TestOversizedResultDoesNotCorruptWorkerOutput(t *testing.T) {
	var output bytes.Buffer
	// Each field fits its own byte budget, but JSON escaping and duplicate
	// task identities put their combined frame over the pipe's scanner cap.
	taskID, parentID := strings.Repeat("\x00", 1024), strings.Repeat("\x00", 775)
	oversized := workerproto.Result{RunID: "run", Kind: "agent_task", TaskID: taskID, ParentTaskID: parentID, Depth: 4,
		Task: &workerproto.AgentTask{ID: taskID, ParentID: parentID, Title: "x" + strings.Repeat("\x00", workerproto.MaxPromptBytes-1), Status: "started"}}
	if err := writeResult(&output, oversized); err == nil || output.Len() != 0 {
		t.Fatalf("oversized frame reached the pipe: %d bytes, %v", output.Len(), err)
	}
	completion := workerproto.Result{RunID: "run", Status: "failed", Content: "Progress could not be recorded"}
	if err := writeResult(&output, completion); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(&output)
	scanner.Buffer(make([]byte, 4096), workerproto.MaxResultFrame)
	if !scanner.Scan() {
		t.Fatal("the error result could not be read after rejecting the oversized frame", scanner.Err())
	}
	var decoded workerproto.Result
	if err := json.Unmarshal(scanner.Bytes(), &decoded); err != nil || decoded.RunID != completion.RunID || decoded.Status != "failed" || decoded.Content != completion.Content {
		t.Fatalf("error result was corrupted: %+v, %v", decoded, err)
	}
	if scanner.Scan() || scanner.Err() != nil {
		t.Fatal("the Worker pipe contains extra or broken frames", scanner.Err())
	}
}
