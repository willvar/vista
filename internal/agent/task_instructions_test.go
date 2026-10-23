package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"vista/internal/workerproto"
)

func TestDelegateRetainsCompleteTaskInstructions(t *testing.T) {
	const intro = "请检查实现并给出证据。\n逐项验证下面的任务说明：\n"
	instructions := intro + strings.Repeat("检查中文、多行说明、事件开始与完成后的完整记录。\n", 20)
	maximum := intro + strings.Repeat("x", 2048-len(intro))
	for _, task := range []string{instructions, maximum} {
		t.Run(strconv.Itoa(len(task)), func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			rootPrompt := strings.Repeat("Main task remains a short heading. ", 15)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Messages []struct{ Role, Content string }
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) < 2 {
					t.Errorf("invalid mock request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if system := request.Messages[0].Content; strings.Count(system, "Default to replies in") != 1 || !strings.Contains(system, "Default to replies in Traditional Chinese (Taiwan)") || strings.Contains(system, "Match the task's language") {
					t.Errorf("main or child language policy was lost or contradicted: %s", system)
				}
				if request.Messages[1].Content == task {
					continuationAnswer(w, "child complete")
					return
				}
				if request.Messages[1].Content != rootPrompt {
					t.Errorf("model received modified prompt (%d bytes)", len(request.Messages[1].Content))
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if request.Messages[len(request.Messages)-1].Role == "user" {
					arguments, _ := json.Marshal(map[string]string{"task": task})
					continuationEvent(w, mcpToolEvent("instructions-child", "delegate", string(arguments)))
					return
				}
				var result delegationResult
				if json.Unmarshal([]byte(request.Messages[len(request.Messages)-1].Content), &result) == nil && result.Status == "running" {
					continuationEvent(w, mcpToolEvent("collect-instructions", "await_agent", `{"taskIds":["main/instructions-child"]}`))
					return
				}
				continuationAnswer(w, "parent complete")
			}))
			defer server.Close()
			childStarted, childFinished, delegateFinished := false, false, false
			command := continuationCommand(server.URL, rootPrompt)
			command.ReplyLanguage = "zh-TW"
			answer, err := Run(context.Background(), command, func(event workerproto.Result) error {
				if event.Kind == "agent_task" && event.Task != nil {
					if event.Task.ID == "main" {
						if event.Task.Title != textPrefix(rootPrompt, 300) {
							t.Errorf("main heading was expanded: %d bytes", len(event.Task.Title))
						}
					} else {
						if event.Task.Title != task || event.Task.ParentID != "main" {
							t.Errorf("child task instructions truncated: got=%d want=%d", len(event.Task.Title), len(task))
						}
						childStarted = childStarted || event.Task.Status == "started"
						childFinished = childFinished || event.Task.Status == "succeeded"
					}
				}
				if event.Kind == "tool" && event.Tool != nil && event.Tool.Name == "delegate" && event.Status == "succeeded" {
					delegateFinished = true
				}
				return nil
			})
			if err != nil || answer != "parent complete" || !childStarted || !childFinished || !delegateFinished {
				t.Fatalf("complete task flow: answer=%q started=%v finished=%v delegate=%v error=%v", answer, childStarted, childFinished, delegateFinished, err)
			}
		})
	}
}
