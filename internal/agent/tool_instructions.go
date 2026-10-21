package agent

import (
	"context"

	"vista/internal/provider"
	"vista/internal/tools"
)

var listSkillsSchema = map[string]any{"type": "function", "function": map[string]any{"name": "list_skills", "description": "List SKILL.md instructions in immediate child directories of an explicit absolute directory. Returns names and descriptions, not full skill bodies.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"directory": map[string]any{"type": "string"}}, "required": []string{"directory"}}}}

func (r *toolRuntime) listSkills(ctx context.Context, call provider.ToolCall) (string, error) {
	var input struct {
		Directory string `json:"directory"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	list, err := tools.DiscoverSkills(input.Directory)
	if err != nil {
		return "", err
	}
	return marshalResult(list)
}

var readSkillSchema = map[string]any{"type": "function", "function": map[string]any{"name": "read_skill", "description": "Read a page of a discovered SKILL.md. Use nextOffset to continue only when needed.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "offset": map[string]any{"type": "integer", "minimum": 0}}, "required": []string{"path"}}}}

func (r *toolRuntime) readSkill(ctx context.Context, call provider.ToolCall) (string, error) {
	var input struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	page, err := tools.ReadSkill(input.Path, input.Offset)
	if err != nil {
		return "", err
	}
	return marshalResult(page)
}

var projectInstructionsSchema = map[string]any{"type": "function", "function": map[string]any{"name": "project_instructions", "description": "Find applicable AGENTS.md, CLAUDE.md, and Copilot instruction files for a target absolute path, ordered from outer to nearest.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}}}}

func (r *toolRuntime) projectInstructions(ctx context.Context, call provider.ToolCall) (string, error) {
	var input struct {
		Path string `json:"path"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	files, err := tools.DiscoverProjectInstructions(input.Path)
	if err != nil {
		return "", err
	}
	return marshalResult(files)
}

var readProjectInstructionSchema = map[string]any{"type": "function", "function": map[string]any{"name": "read_project_instruction", "description": "Read a page of a discovered project instruction file. Use nextOffset if more content is needed.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "offset": map[string]any{"type": "integer", "minimum": 0}}, "required": []string{"path"}}}}

func (r *toolRuntime) readProjectInstruction(ctx context.Context, call provider.ToolCall) (string, error) {
	var input struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	page, err := tools.ReadProjectInstruction(input.Path, input.Offset)
	if err != nil {
		return "", err
	}
	return marshalResult(page)
}
