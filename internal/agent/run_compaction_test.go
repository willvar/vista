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
	"vista/internal/workerproto"
)

func TestRunCompactsModelWindowWithoutLosingInputsOrReplayingTools(t *testing.T) {
	for _, limits := range []struct {
		name                string
		context, inputLimit int
	}{
		{"context window", 12000, 0},
		{"smaller input window", 200000, 12000},
	} {
		t.Run(limits.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", root)
			config := filepath.Join(root, "vista")
			if err := os.MkdirAll(config, 0700); err != nil {
				t.Fatal(err)
			}
			const instructions = "Retain the finding marker and do not repeat completed writes."
			if err := os.WriteFile(filepath.Join(config, "AGENTS.md"), []byte(instructions), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "input.txt")
			fileContent := strings.Repeat("x", 1536)
			if err := os.WriteFile(path, []byte(fileContent), 0600); err != nil {
				t.Fatal(err)
			}
			writtenPath := filepath.Join(root, "written.txt")
			const prompt = "ORIGINAL_TASK: read the input repeatedly, then report the saved finding."
			const interjection = "INTERJECTED_CONSTRAINT: keep the original requirement and the latest read."
			const toolCount = 100
			inputs := make(chan workerproto.Command, 1)
			var requests, summaries atomic.Int32
			overflowAt := map[int]bool{}
			overflows := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Instructions string           `json:"instructions"`
					Input        []map[string]any `json:"input"`
					Tools        []any            `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if strings.HasPrefix(body.Instructions, "Compress the completed execution transcript") {
					summaries.Add(1)
					if len(body.Tools) != 0 {
						t.Error("compaction requested tools")
					}
					compactionResponse(w, []any{compactionText("saved finding")})
					return
				}
				turn := int(requests.Add(1)) - overflows
				if turn > 1 && turn%25 == 0 && !overflowAt[turn] {
					overflowAt[turn] = true
					overflows++
					w.WriteHeader(400)
					fmt.Fprint(w, `{"error":{"code":"context_length_exceeded"}}`)
					return
				}
				if !strings.Contains(body.Instructions, systemPrompt) || !strings.Contains(body.Instructions, instructions) {
					t.Error("original system or global instructions were lost")
				}
				users := map[string]bool{}
				for _, item := range body.Input {
					if item["role"] == "user" {
						text, _ := item["content"].(string)
						users[text] = true
					}
				}
				if !users[prompt] || turn > 1 && !users[interjection] {
					t.Errorf("user inputs lost at model request %d", turn)
				}
				if err := checkCompactionToolPairs(body.Input, turn-1, "1: "+fileContent); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if summaries.Load() > 0 {
					encoded, _ := json.Marshal(body.Input)
					if !strings.Contains(body.Instructions+string(encoded), "saved finding") {
						t.Error("compaction summary missing from resumed model context")
					}
				}
				if turn > toolCount {
					compactionResponse(w, []any{compactionText("Completed with saved finding.")})
					return
				}
				name := "read_file"
				arguments := map[string]string{"path": path}
				if turn == 1 {
					inputs <- workerproto.Command{Action: "interject", RunID: "compact-run", InputID: "constraint", Prompt: interjection}
					name = "write_file"
					arguments = map[string]string{"path": writtenPath, "expectedSha256": "missing", "content": "written once"}
				}
				encoded, _ := json.Marshal(arguments)
				compactionResponse(w, []any{
					map[string]any{"type": "reasoning", "id": fmt.Sprintf("rs_%d", turn), "summary": []any{}, "encrypted_content": fmt.Sprintf("signature_%d", turn)},
					map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%d", turn), "call_id": fmt.Sprintf("call_%d", turn), "name": name, "arguments": string(encoded)},
				})
			}))
			defer server.Close()
			command := workerproto.Command{RunID: "compact-run", Prompt: prompt, Model: modelcfg.Model{
				Provider: "openai-responses", ID: "window-test", APIKey: "test-key", BaseURL: server.URL + "/v1",
				Context: limits.context, InputLimit: limits.inputLimit,
			}}
			started, succeeded := map[string]int{}, map[string]int{}
			summaryEvents, deliveries := 0, 0
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			answer, err := RunWithInputs(ctx, command, func(event workerproto.Result) error {
				if event.Kind == "model_request" && event.ModelUse != nil && event.ModelUse.Purpose == "summary" {
					summaryEvents++
				}
				if event.Kind == "input_delivered" && event.InputID == "constraint" {
					deliveries++
				}
				if event.Kind != "tool" {
					return nil
				}
				switch event.Status {
				case "started":
					started[event.Tool.CallID]++
				case "succeeded":
					succeeded[event.Tool.CallID]++
				default:
					return fmt.Errorf("tool %s failed during compaction", event.Tool.CallID)
				}
				return nil
			}, inputs)
			if err != nil || answer != "Completed with saved finding." || requests.Load() != toolCount+1+int32(overflows) {
				t.Fatalf("run answer=%q requests=%d summaries=%d err=%v", answer, requests.Load(), summaries.Load(), err)
			}
			if summaries.Load() < 3 || int(summaries.Load()) != summaryEvents || deliveries != 1 {
				t.Fatalf("compaction summaries=%d events=%d input deliveries=%d", summaries.Load(), summaryEvents, deliveries)
			}
			if len(started) != toolCount || len(succeeded) != toolCount {
				t.Fatalf("tools started=%d succeeded=%d", len(started), len(succeeded))
			}
			for id := 1; id <= toolCount; id++ {
				key := fmt.Sprintf("call_%d", id)
				if started[key] != 1 || succeeded[key] != 1 {
					t.Fatalf("completed tool %s was omitted or replayed", key)
				}
			}
			if data, err := os.ReadFile(writtenPath); err != nil || string(data) != "written once" {
				t.Fatalf("write side effect changed: content=%q err=%v", data, err)
			}
		})
	}
}

func checkCompactionToolPairs(input []map[string]any, latest int, content string) error {
	calls, outputs, reasoning := map[string]int{}, map[string]string{}, map[string]int{}
	for _, item := range input {
		id, _ := item["call_id"].(string)
		switch item["type"] {
		case "function_call":
			calls[id]++
		case "function_call_output":
			if _, exists := outputs[id]; exists {
				return fmt.Errorf("duplicate tool output %s", id)
			}
			outputs[id], _ = item["output"].(string)
		case "reasoning":
			signature, _ := item["encrypted_content"].(string)
			reasoning[strings.Replace(signature, "signature_", "call_", 1)]++
		}
	}
	if len(calls) != len(outputs) || len(calls) != len(reasoning) {
		return fmt.Errorf("orphaned tool or opaque reasoning item: calls=%d outputs=%d reasoning=%d", len(calls), len(outputs), len(reasoning))
	}
	for id, count := range calls {
		var result map[string]any
		if count != 1 || reasoning[id] != 1 || json.Unmarshal([]byte(outputs[id]), &result) != nil || result["error"] != nil {
			return fmt.Errorf("incomplete retained assistant/tool group %s", id)
		}
		if id != "call_1" && result["content"] != content {
			return fmt.Errorf("retained tool output %s was changed", id)
		}
	}
	if latest > 0 && calls[fmt.Sprintf("call_%d", latest)] != 1 {
		return fmt.Errorf("latest completed assistant/tool group %d was dropped", latest)
	}
	return nil
}

func compactionText(text string) map[string]any {
	return map[string]any{"type": "message", "id": "answer", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": text}}}
}

func compactionResponse(w http.ResponseWriter, output []any) {
	encoded, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": output}})
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: %s\n\n", encoded)
}
