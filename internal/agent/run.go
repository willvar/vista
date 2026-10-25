package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"vista/internal/modeltrace"
	"vista/internal/provider"
	"vista/internal/replylanguage"
	"vista/internal/resources"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

const maxToolResult = 50 * 1024
const maxToolResultLines = 2000

func Run(ctx context.Context, command workerproto.Command, emit func(workerproto.Result) error) (string, error) {
	return RunWithInputs(ctx, command, emit, nil)
}

func RunWithInputs(ctx context.Context, command workerproto.Command, emit func(workerproto.Result) error, inputs <-chan workerproto.Command) (string, error) {
	return RunWithJobs(ctx, command, emit, inputs, nil)
}

func RunWithJobs(ctx context.Context, command workerproto.Command, emit func(workerproto.Result) error, inputs <-chan workerproto.Command, jobs *tools.BackgroundManager) (string, error) {
	return runWithInputSource(ctx, command, emit, inputs, nil, jobs)
}

// The Server supplies an already committed input at each decision boundary.
// No copied Worker queue can become stale after an acknowledged edit/withdrawal.
func RunWithInputSource(ctx context.Context, command workerproto.Command, emit func(workerproto.Result) error, nextInput func(*workerproto.CompletedReply) (*workerproto.Command, error), jobs *tools.BackgroundManager) (string, error) {
	return runWithInputSource(ctx, command, emit, nil, nextInput, jobs)
}

func runWithInputSource(ctx context.Context, command workerproto.Command, emit func(workerproto.Result) error, inputs <-chan workerproto.Command, nextInput func(*workerproto.CompletedReply) (*workerproto.Command, error), jobs *tools.BackgroundManager) (string, error) {
	scope, err := newRunScope(nil)
	if err != nil {
		return "", err
	}
	// Standalone callers have no session lifetime beyond this call.
	scope.delegations.sessionCtx = ctx
	defer scope.delegations.close()
	return runWithScope(ctx, command, emit, inputs, nextInput, jobs, scope)
}

func runWithScope(ctx context.Context, command workerproto.Command, emit func(workerproto.Result) error, inputs <-chan workerproto.Command, nextInput func(*workerproto.CompletedReply) (*workerproto.Command, error), jobs *tools.BackgroundManager, scope *runScope) (string, error) {
	var emitMu sync.Mutex
	safeEmit := func(result workerproto.Result) error {
		emitMu.Lock()
		defer emitMu.Unlock()
		return emit(result)
	}
	return run(ctx, command, safeEmit, scope, 0, inputs, nextInput, jobs, false)
}

func run(ctx context.Context, command workerproto.Command, emit func(workerproto.Result) error, scope *runScope, depth int, inputs <-chan workerproto.Command, nextInput func(*workerproto.CompletedReply) (*workerproto.Command, error), jobs *tools.BackgroundManager, taskStarted bool) (answer string, runErr error) {
	ctx = replylanguage.WithDefault(ctx, command.ReplyLanguage)
	if command.TaskID == "" {
		command.TaskID = "main"
	}
	parentEmit := emit
	emit = func(result workerproto.Result) error {
		if result.TaskID == "" {
			result.TaskID, result.ParentTaskID, result.Depth = command.TaskID, command.ParentTaskID, depth
			prefix := ""
			if depth > 0 {
				prefix = command.TaskID + "/"
			}
			if result.Tool != nil {
				copy := *result.Tool
				copy.CallID = prefix + copy.CallID
				if depth > 0 {
					copy.Name = "subagent/" + copy.Name
				}
				result.Tool = &copy
			}
			if result.Artifact != nil {
				copy := *result.Artifact
				copy.CallID = prefix + copy.CallID
				copy.TaskID, copy.ParentTaskID = command.TaskID, command.ParentTaskID
				result.Artifact = &copy
			}
		}
		return parentEmit(result)
	}
	task := workerproto.AgentTask{ID: command.TaskID, ParentID: command.ParentTaskID, Join: command.Join, Title: textPrefix(command.Prompt, 300), Status: "started"}
	if depth > 0 {
		task.Title = command.Prompt
	}
	startedTask := task
	var report *workerproto.OutputRef
	defer func() {
		task.Status, task.Report = "succeeded", report
		if runErr != nil {
			task.Status = "failed"
			if ctx.Err() != nil {
				task.Status = "canceled"
			}
		}
		if err := emit(workerproto.Result{RunID: command.RunID, Kind: "agent_task", Task: &task}); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}()
	// Required/best-effort descendants finish with their parent; detached tasks
	// remain owned by the Session and keep their original trace identity.
	defer scope.delegations.closeChildren(command.TaskID)
	if !taskStarted {
		if err := emit(workerproto.Result{RunID: command.RunID, Kind: "agent_task", Task: &startedTask}); err != nil {
			return "", err
		}
	}
	purpose := provider.PurposeConversation
	if depth > 0 {
		purpose = provider.PurposeSubagent
	}
	command.Model = provider.ForPurpose(command.Model, purpose)
	mcp := newMCPManager(command.MCPServers)
	defer mcp.close()
	prepared, err := prepareContext(ctx, command, emit, depth, scope.delegationLimit())
	if err != nil {
		return "", err
	}
	messages := prepared.messages
	// Keep the display aligned with the retained context at decision boundaries,
	// including the final answer and cancellation after completed tools.
	defer func() {
		runErr = errors.Join(runErr, emitContextUsage(ctx, command, messages, prepared.applyModel(messages, command.Model), depth, emit))
	}()
	runChild := func(ctx context.Context, child workerproto.Command, childEmit func(workerproto.Result) error) (string, error) {
		return run(ctx, child, childEmit, scope, depth+1, nil, nil, jobs, true)
	}
	compactor := newExecutionCompactor(messages)
	overflowRecoveries := 0
	consumeInputs := func(reply *workerproto.CompletedReply) (int, error) {
		count := 0
		for {
			var input workerproto.Command
			if nextInput != nil {
				pending, err := nextInput(reply)
				if err != nil {
					return count, err
				}
				if pending == nil {
					return count, nil
				}
				input = *pending
			} else {
				select {
				case pending, ok := <-inputs:
					if !ok {
						return count, nil
					}
					input = pending
				default:
					return count, nil
				}
			}
			if input.InputID == "" || input.RunID != command.RunID || input.Prompt == "" && len(input.Images) == 0 {
				continue
			}
			if nextInput == nil {
				if err := emit(workerproto.Result{RunID: command.RunID, Kind: "input_delivered", InputID: input.InputID, Reply: reply}); err != nil {
					return count, err
				}
			}
			reply = nil
			command.MessageSequence = max(command.MessageSequence, input.MessageSequence)
			command.Debug = input.Debug
			if input.ReplyLanguage != "" {
				command.ReplyLanguage = input.ReplyLanguage
				ctx = replylanguage.WithDefault(ctx, input.ReplyLanguage)
			}
			messages = append(messages, (provider.ChatMessage{Role: "user", Content: input.Prompt, Resources: input.Images}).WithVisualInput(true))
			if nextInput == nil && input.Model.Provider != "" {
				command.Model = provider.ForPurpose(input.Model, purpose)
			}
			count++
		}
	}
turnLoop:
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if _, err := consumeInputs(nil); err != nil {
			return "", err
		}
		offeredTools := prepared.applyModel(messages, command.Model)
		requestEstimate := provider.EstimateRequestTokens(ctx, messages, offeredTools)
		if err := emitContextUsage(ctx, command, messages, offeredTools, depth, emit); err != nil {
			return "", err
		}
		providerID := command.Model.ProviderID
		if providerID == "" {
			providerID = command.Model.Provider
		}
		turnID := scope.modelTurns.Add(1)
		if err := emit(workerproto.Result{RunID: command.RunID, Kind: "model_request", Turn: turnID, Depth: depth, ModelUse: &workerproto.ModelUse{Provider: providerID, ID: command.Model.ID, Key: command.Model.Key, ReasoningEffort: provider.ModelReasoningEffort(command.Model)}}); err != nil {
			return "", err
		}
		var pendingStream strings.Builder
		pendingKind := ""
		var lastFlush time.Time
		flushStream := func() error {
			if pendingStream.Len() == 0 {
				return nil
			}
			fragment := pendingStream.String()
			pendingStream.Reset()
			lastFlush = time.Now()
			return emit(workerproto.Result{RunID: command.RunID, Kind: pendingKind, Turn: turnID, Depth: depth, Content: fragment})
		}
		appendStream := func(kind, fragment string) error {
			if pendingKind != kind {
				if err := flushStream(); err != nil {
					return err
				}
				pendingKind = kind
			}
			for fragment != "" {
				n := min(len(fragment), 2048)
				for n > 0 && !utf8.ValidString(fragment[:n]) {
					n--
				}
				if n == 0 {
					return errors.New("model stream has an invalid UTF-8 boundary")
				}
				if pendingStream.Len()+n > 2048 {
					if err := flushStream(); err != nil {
						return err
					}
				}
				pendingStream.WriteString(fragment[:n])
				fragment = fragment[n:]
				if pendingStream.Len() >= 1024 || time.Since(lastFlush) >= 250*time.Millisecond {
					if err := flushStream(); err != nil {
						return err
					}
				}
			}
			return nil
		}
		modelStarted := time.Now()
		var firstTextMS *float64
		requestMessages, requestErr := materializeResources(ctx, command, messages)
		if requestErr != nil {
			return "", requestErr
		}
		requestCtx := withModelDiagnostics(ctx, modeltrace.Scope{Enabled: command.Debug, SessionID: command.SessionID, RunID: command.RunID, TaskID: command.TaskID, Turn: turnID, Purpose: "conversation"}, emit)
		turn, err := provider.ModelTurnStreamWithReasoning(requestCtx, command.Model, requestMessages, offeredTools,
			func(fragment string) error {
				if firstTextMS == nil && strings.TrimSpace(fragment) != "" {
					firstTextMS = firstTextLatency(modelStarted)
				}
				return appendStream("model_delta", fragment)
			},
			func(fragment string) error { return appendStream("model_reasoning_delta", fragment) })
		modelDurationMS := time.Since(modelStarted).Seconds() * 1000
		if observeErr := observeModelRequest(command, turnID, depth, requestEstimate, firstTextMS, turn, err, emit); observeErr != nil {
			return "", observeErr
		}
		if flushErr := flushStream(); flushErr != nil {
			return "", flushErr
		}
		if err != nil {
			if provider.IsContextOverflow(err) {
				if overflowRecoveries >= 2 {
					return "", fmt.Errorf("context overflow persisted after two compaction attempts: %w", err)
				}
				overflowRecoveries++
				next, compacted, compactErr := compactor.compact(ctx, command, messages, offeredTools, emit, scope, depth)
				if compactErr != nil {
					return "", fmt.Errorf("could not recover context overflow: %w", errors.Join(err, compactErr))
				}
				if compacted {
					messages = next
					continue turnLoop
				}
			}
			return "", err
		}
		overflowRecoveries = 0
		// A successful request has seen its selected images. Keep only references
		// for later turns; failed requests retain activation for overflow retry.
		for i := range messages {
			messages[i] = messages[i].WithVisualInput(false)
		}
		if turn.FinishReason != "" || turn.Usage != nil {
			result := &workerproto.ModelResult{Provider: providerID, ID: command.Model.ID, FinishReason: turn.FinishReason, DurationMS: modelDurationMS, ReasoningEffort: provider.ModelReasoningEffort(command.Model)}
			if turn.Usage != nil {
				result.Usage = &workerproto.ModelUsage{InputTokens: turn.Usage.InputTokens, OutputTokens: turn.Usage.OutputTokens, CacheReadTokens: turn.Usage.CacheReadTokens, ReasoningTokens: turn.Usage.ReasoningTokens}
			}
			if err := emit(workerproto.Result{RunID: command.RunID, Kind: "model_result", Turn: turnID, Depth: depth, ModelResult: result}); err != nil {
				return "", err
			}
		}
		for _, source := range turn.Sources {
			if err := emit(workerproto.Result{RunID: command.RunID, Kind: "source", Turn: turnID, Depth: depth, Content: source}); err != nil {
				return "", err
			}
		}
		for _, call := range turn.HostedCalls {
			status := "succeeded"
			if call.Failure {
				status = "failed"
			}
			hostedEmit := func(result workerproto.Result) error {
				if result.TaskID == "" {
					result.Turn = turnID
				}
				return emit(result)
			}
			bounded, err := boundToolOutput(ctx, command.RunID, call.ID, call.Name, call.Result, nil, hostedEmit)
			if err != nil {
				return "", err
			}
			if err := emit(workerproto.Result{RunID: command.RunID, Kind: "tool", Turn: turnID, Depth: depth, Status: status, Content: bounded.event, Tool: &workerproto.ToolProgress{CallID: call.ID, Name: call.Name, Summary: "Executed by Provider", ProviderExecuted: true, OutputRef: bounded.reference, Truncated: bounded.truncated}}); err != nil {
				return "", err
			}
		}
		if len(turn.Calls) == 0 {
			if strings.TrimSpace(turn.Text) == "" {
				if continuation, ok := turn.HostedContinuation(); ok {
					if err := retainModelMessage(command, turnID, "assistant", 0, continuation, depth, emit); err != nil {
						return "", err
					}
					messages = append(messages, continuation)
					continue
				}
				return "", errors.New("model returned an empty answer")
			}
			preview := answerPreview(turn.Text, command.RunID, turnID)
			message := provider.ChatMessage{Role: "assistant", Content: preview}
			if len(turn.Text) <= 8<<10 {
				message = turn.AssistantMessage(preview)
			}
			retained := message
			retained.Content = turn.Text // The next run also retains long formal answers in full.
			if err := retainModelMessage(command, turnID, "assistant", 0, retained, depth, emit); err != nil {
				return "", err
			}
			messages = append(messages, retained)
			if count, err := consumeInputs(&workerproto.CompletedReply{Turn: turnID, FullBytes: len(turn.Text), Content: preview}); err != nil {
				return "", err
			} else if count > 0 {
				continue
			}
			if scope.delegations.hasUncollectedRequired(command.TaskID) {
				results, err := scope.delegations.joinRequired(ctx, command.TaskID)
				if err != nil {
					return "", err
				}
				if len(results) > 0 {
					joined := provider.ChatMessage{Role: "system", Content: coordinatorJoinMessage(results), RuntimeData: true}
					if err := retainModelMessage(command, turnID, "join", 0, joined, depth, emit); err != nil {
						return "", err
					}
					messages = append(messages, joined)
					continue turnLoop
				}
			}
			if depth == 0 {
				if err := emit(workerproto.Result{RunID: command.RunID, Kind: "model_complete", Turn: turnID, FullBytes: len(turn.Text)}); err != nil {
					return "", err
				}
			}
			report = &workerproto.OutputRef{RunID: command.RunID, Turn: turnID, Bytes: int64(len(turn.Text))}
			if depth > 0 {
				return turn.Text, nil
			}
			return preview, nil
		}
		callErrors := make([]error, len(turn.Calls))
		checklistUpdates := 0
		for _, call := range turn.Calls {
			if call.Function.Name == "update_checklist" {
				checklistUpdates++
			}
		}
		for index, call := range turn.Calls {
			if call.Function.Name == "update_checklist" && checklistUpdates > 1 {
				callErrors[index] = errors.New("request only one checklist replacement per turn")
			}
			if call.Function.Name != "question" {
				continue
			}
			if depth != 0 || command.SessionID == "" || len(turn.Calls) != 1 {
				callErrors[index] = errors.New("only the main Agent may ask a question, without other tool calls")
				continue
			}
			pending, err := questionRequest(call)
			if err != nil {
				callErrors[index] = err
				continue
			}
			questionMessage := turn.AssistantMessage(turn.Text)
			if err := retainModelMessage(command, turnID, "question", 0, questionMessage, depth, emit); err != nil {
				return "", err
			}
			messages = append(messages, questionMessage)
			if count, err := consumeInputs(nil); err != nil {
				return "", err
			} else if count > 0 {
				messages = completeInterruptedToolHistory(messages, "")
				continue turnLoop
			}
			if err := emit(workerproto.Result{RunID: command.RunID, Kind: "question", Pending: &pending}); err != nil {
				return "", err
			}
			return pending.Content, nil
		}
		assistantText := turn.Text
		if exceedsToolBudget(assistantText) {
			assistantText = modelToolPreview(assistantText, &workerproto.OutputRef{RunID: command.RunID, Turn: turnID, Bytes: int64(len(turn.Text))})
		}
		assistantMessage := turn.AssistantMessage(assistantText)
		if err := retainModelMessage(command, turnID, "assistant", 0, assistantMessage, depth, emit); err != nil {
			return "", err
		}
		messages = append(messages, assistantMessage)
		if err := emitContextUsage(ctx, command, messages, offeredTools, depth, emit); err != nil {
			return "", err
		}
		outputs := make([]string, len(turn.Calls))
		resourceOutputs := make([][]resources.Ref, len(turn.Calls))
		eventOutputs := make([]string, len(turn.Calls))
		outputRefs := make([]*workerproto.OutputRef, len(turn.Calls))
		truncated := make([]bool, len(turn.Calls))
		statuses := make([]string, len(turn.Calls))
		historyErrors := make([]error, len(turn.Calls))
		for _, call := range turn.Calls {
			summary := toolSummary(call)
			if err := emit(workerproto.Result{RunID: command.RunID, Kind: "tool", Turn: turnID, Status: "started", Tool: &workerproto.ToolProgress{CallID: call.ID, Name: call.Function.Name, Summary: summary, Input: toolInput(call, command)}}); err != nil {
				return "", err
			}
		}
		var wg sync.WaitGroup
		for index, call := range turn.Calls {
			wg.Add(1)
			go func(index int, call provider.ToolCall) {
				defer wg.Done()
				toolEmit := func(result workerproto.Result) error {
					if result.TaskID == "" {
						result.Turn = turnID
					}
					if result.Artifact != nil && result.Artifact.CallID == "" {
						copy := *result.Artifact
						copy.CallID = call.ID
						result.Artifact = &copy
					}
					return emit(result)
				}
				runtime := &toolRuntime{definitions: prepared.tools, mcp: mcp, scope: scope, depth: depth, parent: command, emit: toolEmit, jobs: jobs, runChild: runChild}
				output, toolErr := "", callErrors[index]
				if toolErr == nil {
					output, toolErr = runtime.execute(ctx, call)
				}
				resourceOutputs[index] = runtime.resourceResults
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
				outputs[index], eventOutputs[index] = bounded.model, bounded.event
				outputRefs[index], truncated[index] = bounded.reference, bounded.truncated
				statuses[index] = status
				historyErrors[index] = retainModelMessage(command, turnID, "tool", index, provider.ChatMessage{Role: "tool", ToolCallID: call.ID, Content: bounded.model, Resources: resourceOutputs[index]}, depth, toolEmit)
			}(index, call)
		}
		wg.Wait()
		if err := errors.Join(historyErrors...); err != nil {
			return "", err
		}
		for index, call := range turn.Calls {
			summary := toolSummary(call)
			if err := emit(workerproto.Result{RunID: command.RunID, Kind: "tool", Turn: turnID, Status: statuses[index], Content: eventOutputs[index], Tool: &workerproto.ToolProgress{CallID: call.ID, Name: call.Function.Name, Summary: summary, Input: toolInput(call, command), OutputRef: outputRefs[index], Truncated: truncated[index]}}); err != nil {
				return "", err
			}
			messages = append(messages, (provider.ChatMessage{Role: "tool", ToolCallID: call.ID, Content: outputs[index], Resources: resourceOutputs[index]}).WithVisualInput(true))
		}
	}
}

const maxAnswerPreviewJSONBytes = 24 << 10

func answerPreview(full, runID string, turn int32) string {
	if len(full) <= 8<<10 {
		encoded, _ := json.Marshal(full)
		if len(encoded) <= maxAnswerPreviewJSONBytes {
			return full
		}
	}
	notice := fmt.Sprintf("\n\n[Full answer: %d bytes in model output for run %s, turn %d. Read it by page in the Session.]", len(full), runID, turn)
	low, high := 0, min(len(full), 7<<10)
	best := 0
	for low <= high {
		probe := low + (high-low)/2
		end := probe
		for end > 0 && !utf8.ValidString(full[:end]) {
			end--
		}
		encoded, _ := json.Marshal(full[:end] + notice)
		if len(encoded) <= maxAnswerPreviewJSONBytes {
			best = end
			low = probe + 1
		} else {
			high = probe - 1
		}
	}
	return full[:best] + notice
}
