package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"vista/internal/provider"
	"vista/internal/resources"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

// toolRuntime receives the current command and per-call event sink. A model
// switch or interjection is therefore visible to every subsequent tool call.
// Delegation invokes a runner supplied by the orchestration layer.
type toolRuntime struct {
	resourceResults []resources.Ref
	definitions     toolSet
	mcp             *mcpManager
	scope           *runScope
	depth           int
	parent          workerproto.Command
	emit            func(workerproto.Result) error
	jobs            *tools.BackgroundManager
	runChild        func(context.Context, workerproto.Command, func(workerproto.Result) error) (string, error)
}

type toolGroup uint8

const (
	toolAlways toolGroup = iota
	toolMain
	toolMCP
	toolDelegation
)

type toolDefinition struct {
	schema  map[string]any
	execute func(*toolRuntime, context.Context, provider.ToolCall) (string, error)
	group   toolGroup
}
type toolSet []toolDefinition

// Declarations and handlers share one ordered list, used both to advertise and
// dispatch tools. question is a control-flow operation handled by the run loop.
func builtinTools() toolSet {
	return toolSet{
		{readFileSchema, (*toolRuntime).readFile, toolAlways},
		{readToolOutputSchema, (*toolRuntime).readToolOutput, toolAlways},
		{searchSchema, (*toolRuntime).search, toolAlways},
		{webfetchSchema, (*toolRuntime).webfetch, toolAlways},
		{websearchSchema, (*toolRuntime).websearch, toolAlways},
		{writeFileSchema, (*toolRuntime).writeFile, toolAlways},
		{shellSchema, (*toolRuntime).shell, toolAlways},
		{startBackgroundSchema, (*toolRuntime).startBackground, toolAlways},
		{stopBackgroundSchema, (*toolRuntime).stopBackground, toolAlways},
		{listSkillsSchema, (*toolRuntime).listSkills, toolAlways},
		{readSkillSchema, (*toolRuntime).readSkill, toolAlways},
		{projectInstructionsSchema, (*toolRuntime).projectInstructions, toolAlways},
		{readProjectInstructionSchema, (*toolRuntime).readProjectInstruction, toolAlways},
		{historyIndexSchema, (*toolRuntime).historyIndex, toolAlways},
		{readHistorySchema, (*toolRuntime).readHistory, toolAlways},
		{listResourcesSchema, (*toolRuntime).listResources, toolAlways},
		{readResourceSchema, (*toolRuntime).readResource, toolAlways},
		{readModelOutputSchema, (*toolRuntime).readModelOutput, toolAlways},
		{updateChecklistSchema, (*toolRuntime).updateChecklist, toolMain},
		{memorySchema, (*toolRuntime).memory, toolMain},
		{questionSchema, nil, toolMain},
		{mcpListToolsSchema, (*toolRuntime).mcpListTools, toolMCP},
		{mcpCallToolSchema, (*toolRuntime).mcpCallTool, toolMCP},
		{delegateSchema, (*toolRuntime).delegate, toolDelegation},
		{awaitAgentSchema, (*toolRuntime).awaitAgent, toolDelegation},
		{cancelAgentSchema, (*toolRuntime).cancelAgent, toolDelegation},
	}
}

func (set toolSet) schemas(depth, delegationLimit int, hasMCP bool) []map[string]any {
	schemas := make([]map[string]any, 0, len(set))
	for _, tool := range set {
		if tool.group == toolMain && depth != 0 || tool.group == toolMCP && !hasMCP || tool.group == toolDelegation && depth >= delegationLimit {
			continue
		}
		schemas = append(schemas, tool.schema)
	}
	return schemas
}

func (r *toolRuntime) execute(ctx context.Context, call provider.ToolCall) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	definitions := r.definitions
	if definitions == nil {
		definitions = builtinTools()
	}
	for _, tool := range definitions {
		if tool.schema["function"].(map[string]any)["name"] == call.Function.Name && tool.execute != nil {
			return tool.execute(r, ctx, call)
		}
	}
	return "", fmt.Errorf("unsupported tool %q", call.Function.Name)
}

func decodeArguments(raw string, dst any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("tool arguments contain extra data")
	}
	return nil
}

func marshalResult(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
