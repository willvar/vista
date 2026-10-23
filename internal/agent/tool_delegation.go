package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"vista/internal/provider"
	"vista/internal/workerproto"
)

var delegateSchema = map[string]any{"type": "function", "function": map[string]any{"name": "delegate", "description": "Start a subagent for bounded independent work only when substantial parallel work is likely to reduce completion time or the user explicitly requests independent review. Handle short tasks and quick checks directly. Immediately returns its taskId. It inherits this model, MCP servers, and tools. The default join policy is required, so the runtime collects its report before the parent finalizes. Use best_effort when unfinished work can stop with this parent. Use detached for session-owned background work: it survives parent completion/cancellation and stops on explicit task cancellation, session close, or its own completion/failure.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"task": map[string]any{"type": "string"}, "join": map[string]any{"type": "string", "enum": []string{"required", "best_effort", "detached"}}}, "required": []string{"task"}}}}

func (r *toolRuntime) delegate(ctx context.Context, call provider.ToolCall) (string, error) {
	if r.depth >= r.scope.delegationLimit() {
		return "", fmt.Errorf("subagent delegation depth limit reached (%d)", r.scope.delegationLimit())
	}
	if r.scope == nil || r.scope.delegations == nil {
		return "", errors.New("delegated tasks are unavailable")
	}
	var input struct {
		Task string `json:"task"`
		Join string `json:"join"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	if strings.TrimSpace(input.Task) == "" || len(input.Task) > 2048 {
		return "", errors.New("subagent task must be 1–2048 bytes")
	}
	if input.Join == "" {
		input.Join = "required"
	}
	child := r.parent
	child.Prompt = input.Task
	child.History = nil
	child.Images = nil // Session originals remain available through resource tools.
	child.Join = input.Join
	// Persist registration before returning the task ID. The parent may finish
	// immediately after delegate, before the child's goroutine gets scheduled.
	prepare := func(taskID string) error {
		task := workerproto.AgentTask{ID: taskID, ParentID: r.parent.TaskID, Join: input.Join, Title: input.Task, Status: "started"}
		event := workerproto.Result{RunID: child.RunID, Kind: "agent_task", TaskID: taskID, ParentTaskID: r.parent.TaskID, Depth: r.depth + 1, Task: &task}
		if err := r.emit(event); err != nil {
			task.Status = "canceled"
			_ = r.emit(event)
			return err
		}
		return nil
	}
	result, err := r.scope.delegations.startPrepared(ctx, r.parent.TaskID, call.ID, input.Join, prepare, func(childCtx context.Context, taskID string) (string, *workerproto.OutputRef, error) {
		child.ParentTaskID, child.TaskID = r.parent.TaskID, taskID
		var childReport *workerproto.OutputRef
		childEmit := func(result workerproto.Result) error {
			if result.Kind == "agent_task" && result.Task != nil && result.Task.ID == child.TaskID && result.Task.Status == "succeeded" {
				childReport = result.Task.Report
			}
			return r.emit(result)
		}
		answer, err := r.runChild(childCtx, child, childEmit)
		return answer, childReport, err
	})
	if err != nil {
		return "", err
	}
	return marshalResult(result)
}

var awaitAgentSchema = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "await_agent",
		"description": "Collect the first completed uncollected report among the delegated tasks listed in taskIds. Pass one or more exact task IDs returned by delegate. By default wait until one finishes. Optional timeoutSeconds returns running without canceling; zero checks immediately. Already collected IDs are skipped while others remain, or replay their saved report when all were collected.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"taskIds": map[string]any{
					"type":     "array",
					"items":    map[string]any{"type": "string"},
					"minItems": 1,
				},
				"timeoutSeconds": map[string]any{"type": "integer", "minimum": 0},
			},
			"required": []string{"taskIds"},
		},
	},
}

func (r *toolRuntime) awaitAgent(ctx context.Context, call provider.ToolCall) (string, error) {
	if r.scope == nil || r.scope.delegations == nil {
		return "", errors.New("delegated tasks are unavailable")
	}
	var input struct {
		TaskIDs        []string `json:"taskIds"`
		TimeoutSeconds *int64   `json:"timeoutSeconds"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	if len(input.TaskIDs) == 0 {
		return "", errors.New("await_agent requires at least one taskId")
	}
	result, err := r.scope.delegations.await(ctx, r.parent.TaskID, input.TaskIDs, input.TimeoutSeconds)
	if err != nil {
		return "", err
	}
	return marshalResult(result)
}

var cancelAgentSchema = map[string]any{"type": "function", "function": map[string]any{"name": "cancel_agent", "description": "Cancel one of your delegated tasks and its descendants when no longer needed. Waits for it to stop and returns its final status, or its report if it already finished.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"taskId": map[string]any{"type": "string"}}, "required": []string{"taskId"}}}}

func (r *toolRuntime) cancelAgent(ctx context.Context, call provider.ToolCall) (string, error) {
	if r.scope == nil || r.scope.delegations == nil {
		return "", errors.New("delegated tasks are unavailable")
	}
	var input struct {
		TaskID string `json:"taskId"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	result, err := r.scope.delegations.cancel(ctx, r.parent.TaskID, input.TaskID)
	if err != nil {
		return "", err
	}
	return marshalResult(result)
}
