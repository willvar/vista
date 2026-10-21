package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	modelcfg "vista/internal/model"
)

// Vercel AI Gateway forwards the AI SDK language-model protocol rather than
// OpenAI Chat. The pinned OpenCode version uses specification v3.
func vercelGatewayTurn(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any, onText, onReasoning func(string) error) (Turn, error) {
	if err := validateImageMessages(messages); err != nil {
		return Turn{}, err
	}
	if model.ID == "" || model.APIKey == "" {
		return Turn{}, errors.New("Vercel AI Gateway model and API key are required")
	}
	base := model.BaseURL
	if base == "" {
		base = "https://ai-gateway.vercel.sh/v3/ai"
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) {
		return Turn{}, errors.New("invalid Vercel AI Gateway base URL: HTTPS or loopback HTTP required")
	}
	prompt := make([]map[string]any, 0, len(messages))
	callNames := map[string]string{}
	for _, message := range messages {
		images, err := imagePayloads(message)
		if err != nil {
			return Turn{}, err
		}
		switch message.Role {
		case "system":
			prompt = append(prompt, map[string]any{"role": "system", "content": message.Content})
		case "user":
			parts := make([]map[string]any, 0, len(images)+1)
			if message.Content != "" || len(images) == 0 {
				parts = append(parts, map[string]any{"type": "text", "text": message.Content})
			}
			for i, dataURL := range images {
				parts = append(parts, map[string]any{"type": "file", "mediaType": message.Images[i].MediaType, "data": dataURL})
			}
			prompt = append(prompt, map[string]any{"role": "user", "content": parts})
		case "assistant":
			parts := make([]any, 0, 1+len(message.ToolCalls))
			if message.Content != "" {
				parts = append(parts, map[string]any{"type": "text", "text": message.Content})
			}
			for _, call := range message.ToolCalls {
				if call.ID == "" || call.Function.Name == "" || len(call.Function.Arguments) > maxToolArgsBytes {
					return Turn{}, errors.New("invalid Vercel AI Gateway previous tool call")
				}
				var input map[string]any
				if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil || input == nil {
					return Turn{}, errors.New("invalid Vercel AI Gateway previous tool arguments")
				}
				if callNames[call.ID] != "" {
					return Turn{}, errors.New("duplicate Vercel AI Gateway previous tool call ID")
				}
				callNames[call.ID] = call.Function.Name
				parts = append(parts, map[string]any{"type": "tool-call", "toolCallId": call.ID, "toolName": call.Function.Name, "input": input})
			}
			prompt = append(prompt, map[string]any{"role": "assistant", "content": parts})
		case "tool":
			name := callNames[message.ToolCallID]
			if name == "" {
				return Turn{}, errors.New("Vercel AI Gateway tool result lacks a matching call")
			}
			prompt = append(prompt, map[string]any{"role": "tool", "content": []any{map[string]any{"type": "tool-result", "toolCallId": message.ToolCallID, "toolName": name, "output": map[string]any{"type": "text", "value": message.Content}}}})
		default:
			return Turn{}, errors.New("invalid Vercel AI Gateway message role")
		}
	}
	tools := make([]map[string]any, 0, len(schemas))
	for _, schema := range schemas {
		function, ok := schema["function"].(map[string]any)
		if schema["type"] != "function" || !ok || function["name"] == "" {
			return Turn{}, errors.New("invalid Vercel AI Gateway function schema")
		}
		parameters, ok := function["parameters"].(map[string]any)
		if !ok {
			return Turn{}, errors.New("Vercel AI Gateway tool parameters must be a JSON schema object")
		}
		tool := map[string]any{"type": "function", "name": function["name"], "inputSchema": parameters}
		if description, ok := function["description"].(string); ok && description != "" {
			tool["description"] = description
		}
		tools = append(tools, tool)
	}
	body := map[string]any{"prompt": prompt}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return Turn{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(u.String(), "/")+"/language-model", bytes.NewReader(encoded))
	if err != nil {
		return Turn{}, err
	}
	request.Header.Set("Authorization", "Bearer "+model.APIKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("ai-language-model-specification-version", "3")
	request.Header.Set("ai-language-model-id", model.ID)
	request.Header.Set("ai-language-model-streaming", "true")
	response, err := doModelRequest(request, model)
	if err != nil {
		return Turn{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Turn{}, modelHTTPError("Vercel AI Gateway", response)
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		return Turn{}, errors.New("unexpected Vercel AI Gateway content type")
	}
	return parseVercelGatewayStreamWithReasoning(response.Body, onText, onReasoning)
}

func parseVercelGatewayStream(body io.Reader, onText func(string) error) (Turn, error) {
	return parseVercelGatewayStreamWithReasoning(body, onText, nil)
}

type gatewayStreamUsage struct {
	InputTokens struct {
		Total      *int64 `json:"total"`
		NoCache    *int64 `json:"noCache"`
		CacheRead  *int64 `json:"cacheRead"`
		CacheWrite *int64 `json:"cacheWrite"`
	} `json:"inputTokens"`
	OutputTokens struct {
		Total     *int64 `json:"total"`
		Text      *int64 `json:"text"`
		Reasoning *int64 `json:"reasoning"`
	} `json:"outputTokens"`
}

func gatewayModelUsage(reported *gatewayStreamUsage) (*ModelUsage, error) {
	if reported == nil {
		return nil, nil
	}
	fields := []*int64{
		reported.InputTokens.Total, reported.InputTokens.NoCache, reported.InputTokens.CacheRead, reported.InputTokens.CacheWrite,
		reported.OutputTokens.Total, reported.OutputTokens.Text, reported.OutputTokens.Reasoning,
	}
	observed := false
	for _, field := range fields {
		if field != nil {
			observed = true
			if !validReportedTokens(*field) {
				return nil, errors.New("Vercel AI Gateway reported invalid token usage")
			}
		}
	}
	if !observed {
		return nil, nil
	}
	usage := &ModelUsage{}
	if reported.InputTokens.Total != nil {
		usage.InputTokens = *reported.InputTokens.Total
	}
	if reported.InputTokens.CacheRead != nil {
		usage.CacheReadTokens = *reported.InputTokens.CacheRead
	}
	if reported.OutputTokens.Total != nil {
		usage.OutputTokens = *reported.OutputTokens.Total
	}
	if reported.OutputTokens.Reasoning != nil {
		usage.ReasoningTokens = *reported.OutputTokens.Reasoning
	}
	return usage, nil
}

func parseVercelGatewayStreamWithReasoning(body io.Reader, onText, onReasoning func(string) error) (Turn, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), maxEventBytes)
	var payload strings.Builder
	sawData := false
	var reply strings.Builder
	var calls []ToolCall
	var toolBytes toolCollectionBytes
	pending := map[string]bool{}
	callIDs := map[string]bool{}
	finished := false
	finishReason := ""
	var usage *ModelUsage
	reasoningBytes := 0
	consume := func(data string) error {
		var event struct {
			Type             string `json:"type"`
			ID               string `json:"id"`
			Delta            string `json:"delta"`
			ToolCallID       string `json:"toolCallId"`
			ToolName         string `json:"toolName"`
			Input            string `json:"input"`
			ProviderExecuted bool   `json:"providerExecuted"`
			FinishReason     struct {
				Unified string `json:"unified"`
			} `json:"finishReason"`
			Usage *gatewayStreamUsage `json:"usage"`
			Error json.RawMessage     `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return fmt.Errorf("invalid Vercel AI Gateway stream event: %w", err)
		}
		if finished {
			return errors.New("Vercel AI Gateway stream continued after finish")
		}
		switch event.Type {
		case "text-delta":
			if event.ID == "" || reply.Len()+len(event.Delta) > maxReplyBytes {
				return errors.New("Vercel AI Gateway text delta exceeds limits")
			}
			reply.WriteString(event.Delta)
			if onText != nil && event.Delta != "" {
				return onText(event.Delta)
			}
		case "reasoning-delta":
			if event.ID == "" || len(event.Delta) > maxReplyBytes-reasoningBytes {
				return errors.New("Vercel AI Gateway reasoning delta exceeds limits")
			}
			if event.Delta != "" && onReasoning == nil {
				return errors.New("Vercel AI Gateway reasoning stream requires a sink")
			}
			reasoningBytes += len(event.Delta)
			if onReasoning != nil && event.Delta != "" {
				return onReasoning(event.Delta)
			}
		case "tool-input-start":
			if event.ID == "" || event.ToolName == "" || pending[event.ID] || callIDs[event.ID] {
				return errors.New("invalid Vercel AI Gateway tool input start")
			}
			if err := toolBytes.set(event.ID, len(event.ToolName)); err != nil {
				return err
			}
			pending[event.ID] = true
		case "tool-input-end":
			if event.ID == "" || !pending[event.ID] {
				return errors.New("invalid Vercel AI Gateway tool input end")
			}
		case "tool-call":
			if event.ToolCallID == "" || event.ToolName == "" || len(event.ToolCallID) > 256 || len(event.ToolName) > 128 || len(event.Input) > maxToolArgsBytes || callIDs[event.ToolCallID] || event.ProviderExecuted {
				return errors.New("Vercel AI Gateway tool call exceeds limits or was provider executed")
			}
			var arguments map[string]any
			if err := json.Unmarshal([]byte(event.Input), &arguments); err != nil || arguments == nil {
				return errors.New("invalid Vercel AI Gateway tool arguments")
			}
			var call ToolCall
			call.ID, call.Type, call.Function.Name, call.Function.Arguments = event.ToolCallID, "function", event.ToolName, event.Input
			if err := toolBytes.set(call.ID, len(call.Function.Name)+len(call.Function.Arguments)); err != nil {
				return err
			}
			calls = append(calls, call)
			callIDs[call.ID] = true
			delete(pending, call.ID)
		case "finish":
			if len(pending) != 0 || event.FinishReason.Unified != "stop" && event.FinishReason.Unified != "tool-calls" || event.FinishReason.Unified == "tool-calls" && len(calls) == 0 {
				return errors.New("Vercel AI Gateway stream ended with incomplete output")
			}
			var err error
			usage, err = gatewayModelUsage(event.Usage)
			if err != nil {
				return err
			}
			finished = true
			finishReason = event.FinishReason.Unified
		case "error":
			return modelStreamError("Vercel AI Gateway", []byte(data))
		case "tool-approval-request", "tool-result":
			return errors.New("Vercel AI Gateway returned an unsupported or error event")
		case "text-start", "text-end", "reasoning-start", "reasoning-end", "response-metadata", "stream-start", "tool-input-delta", "raw":
		default:
			return errors.New("unknown Vercel AI Gateway event")
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			sawData = false
			if payload.Len() > 0 {
				data := payload.String()
				payload.Reset()
				if sseKeepalive(data) {
					continue
				}
				if err := consume(data); err != nil {
					return Turn{}, err
				}
			}
			continue
		}
		if fragment, ok := sseDataLine(line); ok {
			separator := 0
			if sawData {
				separator = 1
			}
			if payload.Len()+len(fragment)+separator > maxEventBytes {
				return Turn{}, errors.New("Vercel AI Gateway event exceeds 128 KiB")
			}
			if separator != 0 {
				payload.WriteByte('\n')
			}
			payload.WriteString(fragment)
			sawData = true
		}
	}
	if err := scanner.Err(); err != nil {
		return Turn{}, err
	}
	if payload.Len() > 0 && !sseKeepalive(payload.String()) {
		if err := consume(payload.String()); err != nil {
			return Turn{}, err
		}
	}
	if !finished {
		return Turn{}, errors.New("Vercel AI Gateway stream ended without finish")
	}
	return Turn{Text: reply.String(), Calls: calls, FinishReason: finishReason, Usage: usage}, nil
}
