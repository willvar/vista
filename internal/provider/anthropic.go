package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	modelcfg "vista/internal/model"
)

func AnthropicTurn(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any) (Turn, error) {
	return anthropicTurn(ctx, model, messages, schemas, nil, nil)
}

func anthropicTurn(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any, onText, onReasoning func(string) error) (Turn, error) {
	if err := validateImageMessages(messages); err != nil {
		return Turn{}, err
	}
	if model.ID == "" || model.APIKey == "" && !model.Custom {
		return Turn{}, errors.New("Anthropic model and API key are required")
	}
	if err := requireConfiguredGateway(model); err != nil {
		return Turn{}, err
	}
	base := model.BaseURL
	if base == "" {
		base = "https://api.anthropic.com"
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (model.Custom || isLoopback(u.Hostname())))) {
		return Turn{}, errors.New("invalid Anthropic base URL: HTTPS or loopback HTTP required")
	}
	system, converted, err := anthropicMessages(messages)
	if err != nil {
		return Turn{}, err
	}
	tools, err := anthropicTools(schemas)
	if err != nil {
		return Turn{}, err
	}
	directAnthropic := model.Provider == "anthropic" && (model.ProviderID == "" || model.ProviderID == "anthropic")
	claudeThinking := directAnthropic || model.Provider == "vertex-anthropic" || model.ProviderID == "github-copilot" || ZenCatalogProvider(model.ProviderID) || model.Thinking != nil
	maxTokens := 4096
	if claudeThinking && !model.ReasoningDisabled {
		maxTokens = 32000 // Pinned Anthropic Messages default before the model output limit is applied.
		if model.OutputLimit > 0 {
			maxTokens = min(maxTokens, model.OutputLimit)
		}
	}
	body := map[string]any{"model": requestModelID(model), "max_tokens": maxTokens, "stream": true, "system": system, "messages": converted}
	if claudeThinking && !model.ReasoningDisabled {
		thinking, err := anthropicRequestThinking(model, maxTokens)
		if err != nil {
			return Turn{}, err
		}
		if thinking != nil {
			body["thinking"] = thinking
		}
		if model.Effort != "" {
			body["output_config"] = map[string]any{"effort": model.Effort}
		}
	}
	if !model.ReasoningDisabled && model.ProviderID == "meta" {
		body["thinking"] = map[string]any{"type": "adaptive", "display": "omitted"}
		if model.Thinking != nil {
			thinking := *model.Thinking
			if thinking.BudgetTokens == nil {
				thinking.BudgetTokens = thinking.BudgetAlias
			}
			thinking.BudgetAlias = nil
			if thinking.BudgetTokens != nil && *thinking.BudgetTokens > 2048 {
				budget := 2048 // max_tokens is 4096 for this adapter.
				thinking.BudgetTokens = &budget
			}
			body["thinking"] = thinking
		}
	}
	if !model.ReasoningDisabled && model.Effort != "" {
		body["output_config"] = map[string]any{"effort": model.Effort}
	}
	if model.Provider == "vertex-anthropic" {
		delete(body, "model")
		body["anthropic_version"] = "vertex-2023-10-16"
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return Turn{}, err
	}
	path := strings.TrimRight(u.String(), "/")
	if model.Provider == "vertex-anthropic" {
		path += "/models/" + url.PathEscape(model.ID) + ":streamRawPredict"
	} else {
		if !model.Custom && model.ProviderID != "gitlab" && !strings.HasSuffix(path, "/v1") {
			path += "/v1"
		}
		path += "/messages"
		if directAnthropic {
			path += "?beta=true"
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(encoded))
	if err != nil {
		return Turn{}, err
	}
	if model.AnthropicAuthToken || model.ProviderID == "cloudflare-ai-gateway" || model.Provider == "vertex-anthropic" || model.ProviderID == "gitlab" || model.ProviderID == "github-copilot" || zaiCodingPlanProvider(model.ProviderID) || model.ProviderID == "meta" || moonshotProvider(model.ProviderID) || alibabaProvider(model.ProviderID) || minimaxProvider(model.ProviderID) {
		request.Header.Set("Authorization", "Bearer "+model.APIKey)
	} else if model.APIKey != "" {
		request.Header.Set("x-api-key", model.APIKey)
	}
	gatewayHeader(request, model)
	aihubmixHeader(request, model)
	request.Header.Set("anthropic-version", "2023-06-01")
	betas := "interleaved-thinking-2025-05-14"
	if thinking, ok := body["thinking"].(*modelcfg.ThinkingOption); ok && thinking.BlockBinding != nil {
		betas += ",thinking-binding-controls-2026-08-01"
	}
	request.Header.Set("anthropic-beta", betas)
	copilotHeaders(request, model, messages)
	gitlabHeaders(request, model)
	request.Header.Set("Content-Type", "application/json")
	response, err := doModelRequest(request, model)
	if err != nil {
		return Turn{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Turn{}, modelHTTPError("Anthropic", response)
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		return Turn{}, modelHTTPError("Anthropic (unexpected content type)", response)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), maxEventBytes)
	var text strings.Builder
	type block struct {
		kind, id, name, data            string
		args, text, thinking, signature strings.Builder
	}
	blocks := map[int]*block{}
	completed := map[int]json.RawMessage{}
	completedBytes := 0
	nonToolBlocks := 0
	var calls []ToolCall
	var toolBytes toolCollectionBytes
	callIndices := map[string]int{}
	reasoningBytes := 0
	unsignedThinking := false
	uncapturedText := false
	stopReason := ""
	stopped := false
	var usage *ModelUsage
	type usageEvent struct {
		InputTokens         *int64 `json:"input_tokens"`
		OutputTokens        *int64 `json:"output_tokens"`
		CacheReadTokens     *int64 `json:"cache_read_input_tokens"`
		CacheCreationTokens *int64 `json:"cache_creation_input_tokens"`
		OutputTokenDetails  struct {
			ThinkingTokens *int64 `json:"thinking_tokens"`
		} `json:"output_tokens_details"`
	}
	for {
		payload, ok, err := nextSSEData(scanner, maxEventBytes)
		if err != nil {
			return Turn{}, fmt.Errorf("Anthropic stream read: %w", err)
		}
		if !ok {
			break
		}
		var event struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				Usage *usageEvent `json:"usage"`
			} `json:"message"`
			Usage        *usageEvent `json:"usage"`
			ContentBlock struct {
				Type     string          `json:"type"`
				ID       string          `json:"id"`
				Name     string          `json:"name"`
				Input    json.RawMessage `json:"input"`
				Text     string          `json:"text"`
				Thinking string          `json:"thinking"`
				Data     string          `json:"data"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return Turn{}, errors.New("invalid Anthropic stream event")
		}
		if stopped && event.Type != "ping" {
			return Turn{}, errors.New("Anthropic stream continued after finish")
		}
		switch event.Type {
		case "message_start":
			if event.Message.Usage != nil {
				u := event.Message.Usage
				candidate := ModelUsage{}
				if u.InputTokens != nil {
					if !validReportedTokens(*u.InputTokens) {
						return Turn{}, errors.New("Anthropic reported invalid token usage")
					}
					candidate.InputTokens = *u.InputTokens
				}
				if u.CacheReadTokens != nil {
					if !validReportedTokens(*u.CacheReadTokens) {
						return Turn{}, errors.New("Anthropic reported invalid token usage")
					}
					candidate.CacheReadTokens = *u.CacheReadTokens
					candidate.InputTokens += *u.CacheReadTokens
				}
				if u.CacheCreationTokens != nil {
					if !validReportedTokens(*u.CacheCreationTokens) {
						return Turn{}, errors.New("Anthropic reported invalid token usage")
					}
					candidate.InputTokens += *u.CacheCreationTokens
				}
				if u.OutputTokens != nil {
					candidate.OutputTokens = *u.OutputTokens
				}
				if u.OutputTokenDetails.ThinkingTokens != nil {
					candidate.ReasoningTokens = *u.OutputTokenDetails.ThinkingTokens
				}
				if !validModelUsage(candidate) {
					return Turn{}, errors.New("Anthropic reported invalid token usage")
				}
				usage = &candidate
			}
		case "content_block_start":
			_, active := blocks[event.Index]
			_, finished := completed[event.Index]
			if event.Index < 0 || active || finished {
				return Turn{}, errors.New("Anthropic content blocks exceed limits")
			}
			if event.ContentBlock.Type != "tool_use" {
				if nonToolBlocks >= 64 {
					return Turn{}, errors.New("Anthropic content blocks exceed limits")
				}
				nonToolBlocks++
			}
			item := &block{kind: event.ContentBlock.Type}
			switch item.kind {
			case "tool_use":
				if event.ContentBlock.ID == "" || event.ContentBlock.Name == "" || len(event.ContentBlock.ID) > 128 || len(event.ContentBlock.Name) > 128 || len(event.ContentBlock.Input) > maxToolArgsBytes {
					return Turn{}, errors.New("Anthropic tool call exceeds limits")
				}
				item.id, item.name = event.ContentBlock.ID, event.ContentBlock.Name
				if len(event.ContentBlock.Input) > 0 && string(event.ContentBlock.Input) != "{}" {
					item.args.Write(event.ContentBlock.Input)
				}
				if err := toolBytes.set(fmt.Sprint(event.Index), len(item.id)+len(item.name)+item.args.Len()); err != nil {
					return Turn{}, err
				}
			case "text":
				if text.Len()+len(event.ContentBlock.Text) > maxReplyBytes {
					return Turn{}, errors.New("Anthropic reply exceeds 256 KiB")
				}
				text.WriteString(event.ContentBlock.Text)
				item.text.WriteString(event.ContentBlock.Text)
				if onText != nil && event.ContentBlock.Text != "" {
					if err := onText(event.ContentBlock.Text); err != nil {
						return Turn{}, err
					}
				}
			case "thinking":
				if len(event.ContentBlock.Thinking) > maxReplyBytes-reasoningBytes {
					return Turn{}, errors.New("Anthropic reasoning exceeds 256 KiB")
				}
				if event.ContentBlock.Thinking != "" && onReasoning == nil {
					return Turn{}, errors.New("Anthropic reasoning stream requires a sink")
				}
				reasoningBytes += len(event.ContentBlock.Thinking)
				item.thinking.WriteString(event.ContentBlock.Thinking)
				if onReasoning != nil && event.ContentBlock.Thinking != "" {
					if err := onReasoning(event.ContentBlock.Thinking); err != nil {
						return Turn{}, err
					}
				}
			case "redacted_thinking":
				if len(event.ContentBlock.Data) > maxReplyBytes {
					return Turn{}, errors.New("Anthropic redacted reasoning exceeds 256 KiB")
				}
				item.data = event.ContentBlock.Data
			default:
				return Turn{}, errors.New("unsupported Anthropic content block")
			}
			blocks[event.Index] = item
		case "content_block_delta":
			switch event.Delta.Type {
			case "text_delta":
				if item := blocks[event.Index]; item != nil && item.kind != "text" {
					return Turn{}, errors.New("Anthropic text delta has the wrong content block")
				}
				if text.Len()+len(event.Delta.Text) > maxReplyBytes {
					return Turn{}, errors.New("Anthropic reply exceeds 256 KiB")
				}
				text.WriteString(event.Delta.Text)
				if item := blocks[event.Index]; item != nil {
					item.text.WriteString(event.Delta.Text)
				} else if event.Delta.Text != "" {
					uncapturedText = true
				}
				if onText != nil && event.Delta.Text != "" {
					if err := onText(event.Delta.Text); err != nil {
						return Turn{}, err
					}
				}
			case "thinking_delta":
				item := blocks[event.Index]
				if item == nil || item.kind != "thinking" || len(event.Delta.Thinking) > maxReplyBytes-reasoningBytes {
					return Turn{}, errors.New("Anthropic reasoning exceeds limits")
				}
				if event.Delta.Thinking != "" && onReasoning == nil {
					return Turn{}, errors.New("Anthropic reasoning stream requires a sink")
				}
				reasoningBytes += len(event.Delta.Thinking)
				item.thinking.WriteString(event.Delta.Thinking)
				if onReasoning != nil && event.Delta.Thinking != "" {
					if err := onReasoning(event.Delta.Thinking); err != nil {
						return Turn{}, err
					}
				}
			case "signature_delta":
				item := blocks[event.Index]
				if item == nil || item.kind != "thinking" || item.signature.Len()+len(event.Delta.Signature) > 16<<10 {
					return Turn{}, errors.New("Anthropic thinking signature exceeds limits")
				}
				item.signature.WriteString(event.Delta.Signature)
			case "input_json_delta":
				item := blocks[event.Index]
				if item == nil || item.kind != "tool_use" || item.args.Len()+len(event.Delta.PartialJSON) > maxToolArgsBytes {
					return Turn{}, errors.New("Anthropic tool arguments exceed limits")
				}
				item.args.WriteString(event.Delta.PartialJSON)
				if err := toolBytes.set(fmt.Sprint(event.Index), len(item.id)+len(item.name)+item.args.Len()); err != nil {
					return Turn{}, err
				}
			default:
				return Turn{}, errors.New("unsupported Anthropic content delta")
			}
		case "content_block_stop":
			item := blocks[event.Index]
			if item == nil {
				return Turn{}, errors.New("Anthropic content block ended without start")
			}
			var content map[string]any
			switch item.kind {
			case "tool_use":
				arguments := item.args.String()
				if arguments == "" {
					arguments = "{}"
				}
				var value map[string]any
				_, duplicate := callIndices[item.id]
				if err := json.Unmarshal([]byte(arguments), &value); err != nil || value == nil || duplicate {
					return Turn{}, errors.New("Anthropic tool arguments are incomplete or invalid")
				}
				var call ToolCall
				call.ID, call.Type, call.Function.Name, call.Function.Arguments = item.id, "function", item.name, arguments
				if err := toolBytes.set(fmt.Sprint(event.Index), len(item.id)+len(item.name)+len(arguments)); err != nil {
					return Turn{}, err
				}
				calls = append(calls, call)
				callIndices[item.id] = event.Index
				content = map[string]any{"type": "tool_use", "id": item.id, "name": item.name, "input": value}
			case "text":
				content = map[string]any{"type": "text", "text": item.text.String()}
			case "thinking":
				if item.signature.Len() == 0 {
					unsignedThinking = true
				}
				content = map[string]any{"type": "thinking", "thinking": item.thinking.String()}
				if item.signature.Len() > 0 || !zaiCodingPlanProvider(model.ProviderID) && !moonshotProvider(model.ProviderID) && !alibabaProvider(model.ProviderID) && model.ProviderID != "meta" {
					content["signature"] = item.signature.String()
				}
			case "redacted_thinking":
				content = map[string]any{"type": "redacted_thinking", "data": item.data}
			}
			encoded, err := json.Marshal(content)
			if err != nil {
				return Turn{}, err
			}
			if len(encoded) > 1<<20-completedBytes {
				return Turn{}, errors.New("Anthropic content blocks exceed 1 MiB")
			}
			completedBytes += len(encoded)
			completed[event.Index] = encoded
			delete(blocks, event.Index)
		case "message_delta":
			if event.Delta.StopReason != "" {
				stopReason = event.Delta.StopReason
				if err := contextStopError("Anthropic", stopReason); err != nil {
					return Turn{}, err
				}
			}
			if event.Usage != nil && (event.Usage.OutputTokens != nil || event.Usage.OutputTokenDetails.ThinkingTokens != nil) {
				if usage == nil {
					usage = &ModelUsage{}
				}
				if event.Usage.OutputTokens != nil {
					usage.OutputTokens = *event.Usage.OutputTokens
				}
				if event.Usage.OutputTokenDetails.ThinkingTokens != nil {
					usage.ReasoningTokens = *event.Usage.OutputTokenDetails.ThinkingTokens
				}
				if !validModelUsage(*usage) {
					return Turn{}, errors.New("Anthropic reported invalid token usage")
				}
			}
		case "message_stop":
			stopped = true
		case "error":
			return Turn{}, modelStreamError("Anthropic", event.Error)
		case "ping":
		default:
			return Turn{}, errors.New("unsupported Anthropic stream event")
		}
	}
	if err := scanner.Err(); err != nil {
		return Turn{}, err
	}
	if !stopped || len(blocks) != 0 || (stopReason != "end_turn" && stopReason != "tool_use") || (stopReason == "tool_use" && len(calls) == 0) {
		return Turn{}, errors.New("Anthropic stream ended without a complete response")
	}
	if len(calls) > 0 && unsignedThinking && !zaiCodingPlanProvider(model.ProviderID) && !moonshotProvider(model.ProviderID) && !alibabaProvider(model.ProviderID) && model.ProviderID != "meta" {
		return Turn{}, errors.New("Anthropic tool continuation requires a signed thinking block")
	}
	if len(calls) > 0 && uncapturedText {
		return Turn{}, errors.New("Anthropic tool continuation requires complete text blocks")
	}
	indices := make([]int, 0, len(completed))
	for index := range completed {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	sort.Slice(calls, func(i, j int) bool { return callIndices[calls[i].ID] < callIndices[calls[j].ID] })
	ordered := make([]json.RawMessage, 0, len(indices))
	for _, index := range indices {
		ordered = append(ordered, completed[index])
	}
	if uncapturedText {
		ordered = nil
	}
	return Turn{Text: text.String(), Calls: calls, FinishReason: stopReason, Usage: usage, AnthropicBlocks: ordered}, nil
}

var claudeVersionPattern = regexp.MustCompile(`(?:^|[./])claude-[a-z]+-(\d+)(?:[.-](\d{1,2}))?(?:$|[-:@])`)

func anthropicSupportsThinkingBlockBinding(id string) bool {
	match := claudeVersionPattern.FindStringSubmatch(strings.ToLower(id))
	if match == nil {
		return false
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	return major > 5 || major == 5 && minor >= 1
}

func anthropicRequestThinking(model modelcfg.Model, maxTokens int) (*modelcfg.ThinkingOption, error) {
	if model.Thinking == nil && !anthropicSupportsThinkingBlockBinding(model.ID) {
		return nil, nil
	}
	thinking := modelcfg.ThinkingOption{Type: "adaptive"}
	if model.Thinking != nil {
		thinking = *model.Thinking
		if thinking.BudgetTokens == nil {
			thinking.BudgetTokens = thinking.BudgetAlias
		}
		thinking.BudgetAlias = nil
		if thinking.Type == "enabled" {
			if thinking.BudgetTokens == nil || maxTokens < 2048 {
				return nil, errors.New("Anthropic thinking needs a valid output budget")
			}
			if *thinking.BudgetTokens > maxTokens/2 {
				budget := max(1024, maxTokens/2)
				thinking.BudgetTokens = &budget
			}
		}
	}
	if thinking.Type != "disabled" && anthropicSupportsThinkingBlockBinding(model.ID) {
		binding := modelcfg.ThinkingBlockBinding{PrefixMismatchBehavior: "drop_block"}
		if thinking.BlockBinding != nil && thinking.BlockBinding.PrefixMismatchBehavior != "" {
			binding.PrefixMismatchBehavior = thinking.BlockBinding.PrefixMismatchBehavior
		}
		thinking.BlockBinding = &binding
	}
	return &thinking, nil
}

func minimaxProvider(id string) bool {
	switch id {
	case "minimax", "minimax-cn", "minimax-coding-plan", "minimax-cn-coding-plan":
		return true
	}
	return false
}

func zaiCodingPlanProvider(id string) bool {
	return id == "zai-coding-plan" || id == "zhipuai-coding-plan"
}

func anthropicMessages(messages []ChatMessage) (string, []map[string]any, error) {
	var system strings.Builder
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		images, err := imagePayloads(message)
		if err != nil {
			return "", nil, err
		}
		switch message.Role {
		case "system":
			if system.Len() > 0 {
				system.WriteByte('\n')
			}
			system.WriteString(message.Content)
		case "user":
			if len(images) == 0 {
				out = append(out, map[string]any{"role": "user", "content": message.Content})
				break
			}
			blocks := []any{}
			if message.Content != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": message.Content})
			}
			for i, url := range images {
				blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": message.Images[i].MediaType, "data": imageBase64(url)}})
			}
			out = append(out, map[string]any{"role": "user", "content": blocks})
		case "assistant":
			if len(message.AnthropicBlocks) > 0 {
				for _, block := range message.AnthropicBlocks {
					if len(block) > maxReplyBytes || !json.Valid(block) {
						return "", nil, errors.New("invalid previous Anthropic content block")
					}
				}
				out = append(out, map[string]any{"role": "assistant", "content": message.AnthropicBlocks})
				continue
			}
			blocks := []any{}
			if message.Content != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": message.Content})
			}
			for _, call := range message.ToolCalls {
				var args map[string]any
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil || args == nil {
					return "", nil, errors.New("invalid previous tool arguments")
				}
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": args})
			}
			out = append(out, map[string]any{"role": "assistant", "content": blocks})
		case "tool":
			out = append(out, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": message.ToolCallID, "content": message.Content}}})
		default:
			return "", nil, errors.New("invalid Anthropic message role")
		}
	}
	return system.String(), out, nil
}

func anthropicTools(schemas []map[string]any) ([]map[string]any, error) {
	tools := make([]map[string]any, 0, len(schemas))
	for _, schema := range schemas {
		function, ok := schema["function"].(map[string]any)
		if !ok {
			return nil, errors.New("invalid tool schema")
		}
		tools = append(tools, map[string]any{"name": function["name"], "description": function["description"], "input_schema": function["parameters"]})
	}
	return tools, nil
}
