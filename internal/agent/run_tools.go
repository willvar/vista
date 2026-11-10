package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"vista/internal/provider"
	"vista/internal/resources"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

// This is a scheduling yield, not an execution timeout. Slow work keeps its
// original context/deadline and sends one completion event after its one tool
// reply has returned a running execution ID.
const toolYieldDelay = time.Second

type toolOutcome struct {
	message  provider.ChatMessage
	err      error
	retained bool
}
type toolFuture struct {
	mu       sync.Mutex
	ready    chan struct{}
	result   *toolOutcome
	detached bool
}

func executeToolBatch(ctx context.Context, command workerproto.Command, turn int32, calls []provider.ToolCall, callErrors []error, prepared *preparedContext, mcp *mcpManager, scope *runScope, depth int, jobs *tools.BackgroundManager, runChild func(context.Context, workerproto.Command, func(workerproto.Result) error) (string, error), work *sync.WaitGroup, emit func(workerproto.Result) error) ([]provider.ChatMessage, error) {
	futures := make([]*toolFuture, len(calls))
	for _, call := range calls {
		if err := emit(workerproto.Result{RunID: command.RunID, Kind: "tool", Turn: turn, Status: "started", Tool: &workerproto.ToolProgress{CallID: call.ID, Name: call.Function.Name, Summary: toolSummary(call), Input: toolInput(call, command)}}); err != nil {
			return nil, err
		}
	}
	for index, call := range calls {
		future := &toolFuture{ready: make(chan struct{})}
		futures[index] = future
		instructions := prepared.instructions // immutable acknowledged snapshot for this model turn
		work.Go(func() {
			toolEmit := func(result workerproto.Result) error {
				if result.TaskID == "" {
					result.Turn = turn
				}
				if result.Artifact != nil && result.Artifact.CallID == "" {
					copy := *result.Artifact
					copy.CallID = call.ID
					result.Artifact = &copy
				}
				return emit(result)
			}
			runtime := &toolRuntime{definitions: prepared.tools, mcp: mcp, scope: scope, depth: depth, parent: command, emit: toolEmit, jobs: jobs, runChild: runChild, instructions: &instructions}
			output, toolErr := "", callErrors[index]
			if toolErr == nil {
				output, toolErr = runtime.execute(ctx, call)
			}
			status := "succeeded"
			if toolErr != nil {
				status = "failed"
				output = mergeToolError(output, toolErr)
			}
			bounded, boundErr := boundToolOutput(ctx, command.RunID, call.ID, call.Function.Name, output, resultOutputRef(output), toolEmit)
			if boundErr != nil {
				status = "failed"
				encoded, _ := json.Marshal(map[string]string{"error": boundErr.Error()})
				bounded.model, bounded.event = string(encoded), eventPreview(string(encoded))
			}
			// Each tool publishes its own terminal state as soon as it finishes.
			emitErr := toolEmit(workerproto.Result{RunID: command.RunID, Kind: "tool", Status: status, Content: bounded.event, Tool: &workerproto.ToolProgress{CallID: call.ID, Name: call.Function.Name, Summary: toolSummary(call), Input: toolInput(call, command), OutputRef: bounded.reference, Truncated: bounded.truncated}})
			outcome := &toolOutcome{message: (provider.ChatMessage{Role: "tool", ToolCallID: call.ID, Content: bounded.model, Resources: append([]resources.Ref(nil), runtime.resourceResults...)}).WithVisualInput(true), err: emitErr}
			future.mu.Lock()
			detached := future.detached
			if !detached {
				retained := outcome.message.WithVisualInput(false)
				outcome.err = errors.Join(outcome.err, retainModelMessage(command, turn, "tool", index, retained, depth, toolEmit))
				outcome.retained = true
			}
			future.result = outcome
			close(future.ready)
			future.mu.Unlock()
			if detached {
				preview := bounded.model
				ref := bounded.reference
				var contentOutput *workerproto.OutputRef
				if len(preview) > 8<<10 {
					// Network output and rule pages already applied their own budgets.
					// Restore that exact result instead of letting the runtime notice
					// preview hide user-selected output or rule acknowledgement text.
					if (call.Function.Name == "websearch" || call.Function.Name == "webfetch" || hasInstructionPages(call.Function.Name, bounded.model)) && emitErr == nil {
						contentOutput, emitErr = recordToolOutputStream(ctx, command.RunID, call.ID, func(w io.Writer) error {
							_, err := io.WriteString(w, bounded.model)
							return err
						}, toolEmit)
					}
					if ref == nil && emitErr == nil {
						ref, emitErr = recordToolOutput(ctx, command.RunID, call.ID, []byte(bounded.model), toolEmit)
					}
					if ref != nil {
						preview = modelToolPreviewLimit(bounded.model, ref, 8<<10)
					}
				}
				event := workerproto.RuntimeEvent{ID: command.RunID + ":tool:" + command.TaskID + "/" + call.ID, RunID: command.RunID, Recipient: command.TaskID, Kind: "tool", SourceID: call.ID, Status: status, Content: preview, Output: ref, ContentOutput: contentOutput, Resources: outcome.message.Resources}
				if emitErr == nil {
					emitErr = toolEmit(workerproto.Result{RunID: command.RunID, Kind: "runtime_event", RuntimeEvent: &event})
				}
				if emitErr != nil {
					scope.events.fail(command.RunID, emitErr)
					return
				}
				scope.events.publish(ctx, event)
			}
		})
	}
	deadline := time.NewTimer(toolYieldDelay)
	defer deadline.Stop()
	yielded := false
	messages := make([]provider.ChatMessage, 0, len(calls))
	for index, call := range calls {
		future := futures[index]
		if !yielded {
			select {
			case <-future.ready:
			case <-deadline.C:
				yielded = true
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		future.mu.Lock()
		var outcome toolOutcome
		if future.result != nil {
			outcome = *future.result
		} else {
			eventID := command.RunID + ":tool:" + command.TaskID + "/" + call.ID
			scope.events.mu.Lock()
			scope.events.tools[eventID] = command.TaskID
			scope.events.mu.Unlock()
			future.detached = true
			encoded, _ := json.Marshal(map[string]string{"status": "running", "executionId": eventID, "message": "The tool is still executing. Its final result will arrive as a runtime event; do not rerun or poll the operation."})
			outcome.message = provider.ChatMessage{Role: "tool", ToolCallID: call.ID, Content: string(encoded)}
		}
		future.mu.Unlock()
		if outcome.err != nil {
			return nil, outcome.err
		}
		if !outcome.retained {
			if err := retainModelMessage(command, turn, "tool", index, outcome.message.WithVisualInput(false), depth, emit); err != nil {
				return nil, err
			}
		}
		messages = append(messages, outcome.message)
	}
	return messages, ctx.Err()
}
