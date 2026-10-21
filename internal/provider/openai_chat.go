package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	modelcfg "vista/internal/model"
)

const maxEventBytes = 128 << 10
const maxReplyBytes = 256 << 10

// A single tool may use the retained tool collection budget (for example when
// writing a source file); do not impose a smaller unrelated per-call ceiling.
const maxToolArgsBytes = maxReplyBytes

type chatStreamUsage struct {
	PromptTokens         int64  `json:"prompt_tokens"`
	CompletionTokens     int64  `json:"completion_tokens"`
	ReasoningTokens      int64  `json:"reasoning_tokens"`
	CachedTokens         *int64 `json:"cached_tokens"`
	PromptCacheHitTokens *int64 `json:"prompt_cache_hit_tokens"`
	PromptDetails        struct {
		CachedTokens *int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func chatModelUsage(reported *chatStreamUsage) (ModelUsage, error) {
	cacheRead := reported.PromptDetails.CachedTokens
	if cacheRead == nil {
		cacheRead = reported.PromptCacheHitTokens
	}
	if cacheRead == nil {
		cacheRead = reported.CachedTokens
	}
	reasoningTokens := reported.CompletionDetails.ReasoningTokens
	if reasoningTokens == 0 {
		reasoningTokens = reported.ReasoningTokens
	}
	usage := ModelUsage{InputTokens: reported.PromptTokens, OutputTokens: reported.CompletionTokens, ReasoningTokens: reasoningTokens}
	if cacheRead != nil {
		usage.CacheReadTokens = *cacheRead
	}
	if !validModelUsage(usage) {
		return ModelUsage{}, errors.New("model reported invalid token usage")
	}
	return usage, nil
}

func validReportedTokens(count int64) bool {
	const maxReportedTokens = 1_000_000_000
	return count >= 0 && count <= maxReportedTokens
}

func validModelUsage(usage ModelUsage) bool {
	return validReportedTokens(usage.InputTokens) && validReportedTokens(usage.OutputTokens) && validReportedTokens(usage.CacheReadTokens) && validReportedTokens(usage.ReasoningTokens)
}

func requestModelID(model modelcfg.Model) string {
	if model.WireID != "" {
		return model.WireID
	}
	if model.ProviderID != "cloudflare-ai-gateway" {
		return model.ID
	}
	if strings.HasPrefix(model.ID, "workers-ai/") {
		return strings.TrimPrefix(model.ID, "workers-ai/")
	}
	if strings.HasPrefix(model.ID, "anthropic/") {
		return "anthropic/" + strings.ReplaceAll(strings.TrimPrefix(model.ID, "anthropic/"), ".", "-")
	}
	return model.ID
}

func requireConfiguredGateway(model modelcfg.Model) error {
	if model.ProviderID == "cloudflare-ai-gateway" && model.BaseURL == "" {
		return errors.New("Cloudflare AI Gateway requires a configured base URL")
	}
	return nil
}

func gatewayHeader(req *http.Request, model modelcfg.Model) {
	if model.ProviderID == "cloudflare-ai-gateway" && model.GatewayID != "" {
		req.Header.Set("cf-aig-gateway-id", model.GatewayID)
	}
}

func OpenAIChat(ctx context.Context, model modelcfg.Model, history []ChatMessage, prompt string) (string, error) {
	messages := make([]ChatMessage, 0, len(history)+1)
	for _, m := range history {
		messages = append(messages, ChatMessage{Role: m.Role, Content: m.Content})
	}
	messages = append(messages, ChatMessage{Role: "user", Content: prompt})
	turn, err := OpenAIChatTurn(ctx, model, messages, nil)
	if err != nil {
		return "", err
	}
	if len(turn.Calls) > 0 {
		return "", errors.New("model requested tools but none were offered")
	}
	return turn.Text, nil
}

// OpenAIChatTurn parses a single bounded Chat Completions stream. Tool calls
// are returned only after [DONE] and complete JSON arguments are validated.
func OpenAIChatTurn(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any) (Turn, error) {
	return openAIChatTurn(ctx, model, messages, schemas, nil)
}

func openAIChatTurn(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any, onText func(string) error) (Turn, error) {
	return openAIChatTurnWithReasoning(ctx, model, messages, schemas, onText, nil)
}

func openAIChatTurnWithReasoning(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any, onText, onReasoning func(string) error) (Turn, error) {
	if err := validateImageMessages(messages); err != nil {
		return Turn{}, err
	}
	watsonx := model.Provider == "watsonx"
	sapAI := model.Provider == "sap-ai-core"
	local := model.Provider == "openai-chat" && slices.Contains([]string{"ollama", "lmstudio", "vllm"}, model.ProviderID)
	if model.ID == "" || model.APIKey == "" && !model.Custom && !local && !isBedrockMantle(model) {
		return Turn{}, errors.New("OpenAI Chat model and API key are required")
	}
	if err := requireConfiguredGateway(model); err != nil {
		return Turn{}, err
	}
	if err := requireConfiguredAzure(model); err != nil {
		return Turn{}, err
	}
	base := model.BaseURL
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	u, err := url.Parse(base)
	customChat := model.Custom || model.Provider == "openai-chat" && (model.ProviderID == "" || model.ProviderID == "openai")
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (customChat || isLoopback(u.Hostname())))) {
		return Turn{}, errors.New("invalid OpenAI Chat base URL")
	}
	endpoint := openAIEndpoint(u, "/chat/completions", model)
	if watsonx {
		if model.ProjectID == "" {
			return Turn{}, errors.New("watsonx project ID is required")
		}
		endpoint = strings.TrimRight(u.String(), "/") + "/ml/v1/text/chat_stream?version=2026-04-20"
	}
	if sapAI {
		if model.DeploymentID == "" || model.ResourceGroup == "" {
			return Turn{}, errors.New("SAP AI Core deployment and resource group are required")
		}
		endpoint = strings.TrimRight(u.String(), "/") + "/inference/deployments/" + url.PathEscape(model.DeploymentID) + "/v2/completion"
	}
	for _, m := range messages {
		if m.Role != "user" && m.Role != "assistant" && m.Role != "system" && m.Role != "tool" {
			return Turn{}, errors.New("invalid model message role")
		}
	}
	var wireMessages []map[string]any
	if sapAI {
		wireMessages, err = sapAIMessages(messages)
	} else {
		wireMessages, err = chatMessagesWithReasoningDetails(messages, model.ProviderID)
	}
	if err != nil {
		return Turn{}, err
	}
	body := map[string]any{"model": requestModelID(model), "stream": true, "messages": wireMessages}
	if !watsonx && !sapAI && chatSupportsStore(model) {
		body["store"] = false
	}
	if model.ProviderID != "watsonx" && model.ProviderID != "sap-ai-core" {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	if watsonx {
		body = map[string]any{"model_id": requestModelID(model), "project_id": model.ProjectID, "messages": wireMessages}
	}
	if sapAI {
		prompt := map[string]any{"template": wireMessages}
		if len(schemas) > 0 {
			prompt["tools"] = schemas
		}
		body = map[string]any{"config": map[string]any{"stream": map[string]any{"enabled": true}, "modules": map[string]any{"prompt_templating": map[string]any{"model": map[string]any{"name": requestModelID(model), "params": map[string]any{"stream_options": map[string]any{"include_usage": true}}}, "prompt": prompt}}}}
	}
	if model.ProviderID == "openrouter" || model.ProviderID == "standardcompute" {
		includeUsage := true
		if model.OpenRouter != nil {
			if model.OpenRouter.Usage != nil {
				includeUsage = *model.OpenRouter.Usage
			}
			if model.OpenRouter.Provider != nil {
				body["provider"] = model.OpenRouter.Provider
			}
			if len(model.OpenRouter.Models) > 0 {
				body["models"] = model.OpenRouter.Models
			}
			if len(model.OpenRouter.Plugins) > 0 {
				body["plugins"] = model.OpenRouter.Plugins
			}
			if model.OpenRouter.User != "" {
				body["user"] = model.OpenRouter.User
			}
			if !model.ReasoningDisabled {
				if reasoning := model.OpenRouter.Reasoning; reasoning != nil {
					requestReasoning := map[string]any{}
					if reasoning.Enabled != nil {
						requestReasoning["enabled"] = *reasoning.Enabled
					}
					if reasoning.Exclude != nil {
						requestReasoning["exclude"] = *reasoning.Exclude
					}
					if reasoning.Effort != "" {
						requestReasoning["effort"] = reasoning.Effort
					}
					if reasoning.MaxTokens != nil {
						budget := *reasoning.MaxTokens
						if model.OutputLimit > 0 && budget > model.OutputLimit/2 {
							budget = max(1024, model.OutputLimit/2)
						}
						requestReasoning["max_tokens"] = budget
					}
					body["reasoning"] = requestReasoning
				}
			}
		}
		body["usage"] = map[string]any{"include": includeUsage}
	}
	if !model.ReasoningDisabled && model.ProviderID == "groq" && !strings.HasPrefix(model.ID, "openai/gpt-oss-") {
		body["reasoning_format"] = "parsed"
	}
	if zaiChatProvider(model.ProviderID) {
		if !model.ReasoningDisabled && model.Thinking != nil {
			body["thinking"] = model.Thinking
		}
		if !model.ReasoningDisabled && model.ReasoningEffort != "" {
			body["reasoning_effort"] = model.ReasoningEffort
		}
		if model.ZAIDoSample != nil {
			body["do_sample"] = *model.ZAIDoSample
		}
		if model.ChatResponseFormat != nil {
			body["response_format"] = model.ChatResponseFormat
		}
		if model.ZAIRequestID != "" {
			body["request_id"] = model.ZAIRequestID
		}
		if model.ZAIUserID != "" {
			body["user_id"] = model.ZAIUserID
		}
	}
	if !model.ReasoningDisabled && moonshotProvider(model.ProviderID) && model.ReasoningEffort != "" {
		body["reasoning_effort"] = model.ReasoningEffort
	}
	if !model.ReasoningDisabled && moonshotProvider(model.ProviderID) && model.Thinking != nil {
		body["thinking"] = model.Thinking
	}
	if !model.ReasoningDisabled && model.ProviderID == "xai" && model.ReasoningEffort != "" {
		body["reasoning_effort"] = model.ReasoningEffort
	}
	if !model.ReasoningDisabled && model.ChatReasoningOverride && model.ReasoningEffort != "" {
		body["reasoning_effort"] = model.ReasoningEffort
	}
	if !model.ReasoningDisabled && model.ProviderID == "deepseek" && model.Thinking != nil {
		body["thinking"] = model.Thinking
	}
	if alibabaProvider(model.ProviderID) {
		if !model.ReasoningDisabled && model.ReasoningEffort != "" {
			body["reasoning_effort"] = model.ReasoningEffort
		}
		if model.MaxCompletionTokens != nil {
			body["max_completion_tokens"] = *model.MaxCompletionTokens
		}
		if model.RepetitionPenalty != nil {
			body["repetition_penalty"] = *model.RepetitionPenalty
		}
		if model.ChatResponseFormat != nil {
			body["response_format"] = model.ChatResponseFormat
		}
		if !model.ReasoningDisabled && model.Thinking != nil {
			body["thinking"] = model.Thinking
		}
		if !model.ReasoningDisabled && model.EnableThinking != nil {
			body["enable_thinking"] = *model.EnableThinking
		}
		if !model.ReasoningDisabled && model.ThinkingBudget != nil {
			budget := *model.ThinkingBudget
			if model.MaxCompletionTokens != nil && budget > *model.MaxCompletionTokens/2 {
				budget = max(1, *model.MaxCompletionTokens/2)
			}
			body["thinking_budget"] = budget
		}
		if !model.ReasoningDisabled && model.PreserveThinking != nil {
			body["preserve_thinking"] = *model.PreserveThinking
		}
		if !model.ReasoningDisabled && model.ClearThinking != nil {
			body["clear_thinking"] = *model.ClearThinking
		}
		if model.ParallelToolCalls != nil {
			body["parallel_tool_calls"] = *model.ParallelToolCalls
		}
	}
	if minimaxProvider(model.ProviderID) {
		if !model.ReasoningDisabled && model.Thinking != nil {
			body["thinking"] = model.Thinking
		}
		if !model.ReasoningDisabled {
			reasoningSplit := true
			if model.ReasoningSplit != nil {
				reasoningSplit = *model.ReasoningSplit
			}
			body["reasoning_split"] = reasoningSplit
		}
	}
	if (zaiChatProvider(model.ProviderID) || alibabaProvider(model.ProviderID)) && model.ToolStream != nil {
		body["tool_stream"] = *model.ToolStream
	} else if len(schemas) > 0 && zaiToolStreamModel(model) {
		body["tool_stream"] = true
	}
	if len(schemas) > 0 && !sapAI {
		body["tools"] = chatToolSchemas(model, schemas)
	} else if !sapAI {
		for _, message := range messages {
			if message.Role == "tool" || len(message.ToolCalls) > 0 {
				body["tools"] = []map[string]any{}
				break
			}
		}
	}
	requestBody, err := json.Marshal(body)
	if err != nil {
		return Turn{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return Turn{}, err
	}
	setOpenAIAuth(req, model)
	if model.OpenRouter != nil {
		if model.OpenRouter.AppName != "" {
			req.Header.Set("X-OpenRouter-Title", model.OpenRouter.AppName)
		}
		if model.OpenRouter.AppURL != "" {
			req.Header.Set("HTTP-Referer", model.OpenRouter.AppURL)
		}
		if len(model.OpenRouter.APIKeys) > 0 {
			keys, err := json.Marshal(model.OpenRouter.APIKeys)
			if err != nil {
				return Turn{}, errors.New("invalid OpenRouter provider API keys")
			}
			req.Header.Set("X-Provider-API-Keys", string(keys))
		}
	}
	if model.ProviderID == "xai" && model.PromptCacheKey != "" {
		req.Header.Set("x-grok-conv-id", model.PromptCacheKey)
	}
	copilotHeaders(req, model, messages)
	aihubmixHeader(req, model)
	gatewayHeader(req, model)
	gitlabHeaders(req, model)
	if sapAI {
		req.Header.Set("AI-Resource-Group", model.ResourceGroup)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := doModelRequest(req, model)
	if err != nil {
		return Turn{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Turn{}, modelHTTPError("model", resp)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		return Turn{}, modelHTTPError("model (unexpected content type)", resp)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), maxEventBytes)
	var reply strings.Builder
	reasoningBytes := 0
	var reasoningText strings.Builder
	var observedReasoningField string
	var reasoningDetails chatReasoningDetails
	var data strings.Builder
	sawData := false
	parts := map[int]*ToolCall{}
	var toolBytes toolCollectionBytes
	latestToolIndex := -1
	nextToolIndex := 0
	var sources citationSources
	finishReason := ""
	var usage *ModelUsage
	done := false
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			sawData = false
			if data.Len() == 0 {
				continue
			}
			payload := data.String()
			data.Reset()
			if sseKeepalive(payload) {
				continue
			}
			if payload == "[DONE]" {
				done = true
				break
			}
			type annotation struct {
				Type        string `json:"type"`
				URLCitation struct {
					URL string `json:"url"`
				} `json:"url_citation"`
			}
			type choice struct {
				Delta struct {
					Content          string          `json:"content"`
					Refusal          string          `json:"refusal"`
					ReasoningContent string          `json:"reasoning_content"`
					Reasoning        string          `json:"reasoning"`
					ReasoningText    string          `json:"reasoning_text"`
					ReasoningDetails json.RawMessage `json:"reasoning_details"`
					Annotations      []annotation    `json:"annotations"`
					ToolCalls        []struct {
						Index    *int   `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				Message struct {
					Annotations []annotation `json:"annotations"`
				} `json:"message"`
				FinishReason *string          `json:"finish_reason"`
				Usage        *chatStreamUsage `json:"usage"`
			}
			var event struct {
				Error       json.RawMessage  `json:"error"`
				Choices     []choice         `json:"choices"`
				Citations   []string         `json:"citations"`
				Usage       *chatStreamUsage `json:"usage"`
				FinalResult struct {
					Choices []choice `json:"choices"`
				} `json:"final_result"`
			}
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				return Turn{}, fmt.Errorf("invalid model stream event: %w", err)
			}
			if len(event.Error) > 0 && string(event.Error) != "null" {
				return Turn{}, modelStreamError("model", event.Error)
			}
			for _, source := range event.Citations {
				if err := sources.add(source, "Chat"); err != nil {
					return Turn{}, err
				}
			}
			if event.Usage != nil {
				candidate, err := chatModelUsage(event.Usage)
				if err != nil {
					return Turn{}, err
				}
				usage = &candidate
			}
			choices := event.Choices
			if sapAI {
				choices = event.FinalResult.Choices
			}
			if len(choices) > 1 {
				return Turn{}, errors.New("model returned multiple choices")
			}
			for _, choice := range choices {
				if model.ProviderID == "openrouter" || model.ProviderID == "standardcompute" {
					for _, annotations := range [][]annotation{choice.Delta.Annotations, choice.Message.Annotations} {
						for _, annotation := range annotations {
							if annotation.Type == "url_citation" {
								if err := sources.add(annotation.URLCitation.URL, "OpenRouter"); err != nil {
									return Turn{}, err
								}
							}
						}
					}
				}
				if event.Usage == nil && choice.Usage != nil {
					candidate, err := chatModelUsage(choice.Usage)
					if err != nil {
						return Turn{}, err
					}
					usage = &candidate
				}
				if finishReason != "" && (choice.Delta.Content != "" || choice.Delta.Refusal != "" || choice.Delta.ReasoningContent != "" || choice.Delta.Reasoning != "" || choice.Delta.ReasoningText != "" || len(choice.Delta.ReasoningDetails) > 0 && string(choice.Delta.ReasoningDetails) != "null" || len(choice.Delta.ToolCalls) > 0) {
					return Turn{}, errors.New("model returned content after finish reason")
				}
				reasoning := choice.Delta.ReasoningContent
				if reasoning != "" && observedReasoningField == "" {
					observedReasoningField = "reasoning_content"
				}
				if choice.Delta.Reasoning != "" {
					if reasoning != "" && reasoning != choice.Delta.Reasoning {
						return Turn{}, errors.New("model returned conflicting reasoning fields")
					}
					reasoning = choice.Delta.Reasoning
					if observedReasoningField == "" {
						observedReasoningField = "reasoning"
					}
				}
				if choice.Delta.ReasoningText != "" {
					if reasoning != "" && reasoning != choice.Delta.ReasoningText {
						return Turn{}, errors.New("model returned conflicting reasoning fields")
					}
					reasoning = choice.Delta.ReasoningText
					if observedReasoningField == "" {
						observedReasoningField = "reasoning_text"
					}
				}
				if len(choice.Delta.ReasoningDetails) > 0 && string(choice.Delta.ReasoningDetails) != "null" {
					visible, err := reasoningDetails.add(choice.Delta.ReasoningDetails)
					if err != nil {
						return Turn{}, err
					}
					if visible != "" && (reasoning == "" || hasStructuredVisibleDetail(choice.Delta.ReasoningDetails)) {
						reasoning = visible
					}
				}
				if len(reasoning) > maxReplyBytes-reasoningBytes {
					return Turn{}, errors.New("model reasoning exceeds 256 KiB")
				}
				if reasoning != "" && onReasoning == nil {
					return Turn{}, errors.New("model reasoning stream requires a sink")
				}
				reasoningBytes += len(reasoning)
				reasoningText.WriteString(reasoning)
				if onReasoning != nil && reasoning != "" {
					if err := onReasoning(reasoning); err != nil {
						return Turn{}, err
					}
				}
				if reply.Len()+len(choice.Delta.Content)+len(choice.Delta.Refusal) > maxReplyBytes {
					return Turn{}, errors.New("model reply exceeds 256 KiB")
				}
				reply.WriteString(choice.Delta.Content)
				reply.WriteString(choice.Delta.Refusal)
				if onText != nil && choice.Delta.Content != "" {
					if err := onText(choice.Delta.Content); err != nil {
						return Turn{}, err
					}
				}
				if onText != nil && choice.Delta.Refusal != "" {
					if err := onText(choice.Delta.Refusal); err != nil {
						return Turn{}, err
					}
				}
				for position, fragment := range choice.Delta.ToolCalls {
					index := position
					if len(choice.Delta.ToolCalls) == 1 && latestToolIndex >= 0 {
						index = latestToolIndex
					}
					if fragment.ID != "" {
						for knownIndex, call := range parts {
							if call.ID == fragment.ID {
								index = knownIndex
								break
							}
						}
						if existing := parts[index]; existing != nil && existing.ID != "" && existing.ID != fragment.ID {
							index = nextToolIndex
						}
					}
					if fragment.Index != nil {
						index = *fragment.Index
					}
					if index < 0 {
						return Turn{}, errors.New("model returned a negative tool call index")
					}
					call := parts[index]
					if call == nil {
						call = &ToolCall{Type: "function"}
						parts[index] = call
					}
					if call.ID != fragment.ID {
						call.ID += fragment.ID
					}
					call.Function.Name += fragment.Function.Name
					call.Function.Arguments += fragment.Function.Arguments
					latestToolIndex = index
					nextToolIndex = max(nextToolIndex, index+1)
					if len(call.ID) > 256 || len(call.Function.Name) > 128 || len(call.Function.Arguments) > maxToolArgsBytes || (fragment.Type != "" && fragment.Type != "function") {
						return Turn{}, errors.New("model tool call exceeds limits or has an unsupported type")
					}
					if err := toolBytes.set(fmt.Sprint(index), len(call.ID)+len(call.Function.Name)+len(call.Function.Arguments)); err != nil {
						return Turn{}, err
					}
				}
				if choice.FinishReason != nil {
					finishReason = *choice.FinishReason
					if err := contextStopError("model", finishReason); err != nil {
						return Turn{}, err
					}
				}
			}
			continue
		}
		if fragment, ok := sseDataLine(line); ok {
			separator := 0
			if sawData {
				separator = 1
			}
			if data.Len()+len(fragment)+separator > maxEventBytes {
				return Turn{}, errors.New("model stream event exceeds 128 KiB")
			}
			if separator != 0 {
				data.WriteByte('\n')
			}
			data.WriteString(fragment)
			sawData = true
		}
	}
	if err := scanner.Err(); err != nil {
		return Turn{}, fmt.Errorf("model stream read: %w", err)
	}
	if !done && !((watsonx || sapAI) && finishReason != "") {
		return Turn{}, errors.New("model stream ended before [DONE]")
	}
	if watsonx && finishReason == "eos_token" {
		finishReason = "stop"
	}
	if sapAI {
		switch finishReason {
		case "stop_sequence":
			finishReason = "stop"
		case "function_call", "tool_call", "tool_use":
			finishReason = "tool_calls"
		}
	}
	details, err := reasoningDetails.replay()
	if err != nil {
		return Turn{}, err
	}
	turn := Turn{Text: reply.String(), Sources: sources.urls, FinishReason: finishReason, Usage: usage, ReasoningDetails: details}
	if len(details) > 0 || len(parts) > 0 && reasoningText.Len() > 0 {
		turn.ReasoningText = reasoningText.String()
		turn.ReasoningField = observedReasoningField
		if len(turn.ReasoningText) > maxChatReasoningDetailsBytes {
			return Turn{}, errors.New("model reasoning replay text exceeds 64 KiB")
		}
	}
	if len(parts) == 0 {
		if finishReason != "stop" && finishReason != "end" {
			return Turn{}, chatFinishError(finishReason)
		}
		return turn, nil
	}
	if finishReason != "tool_calls" && finishReason != "function_call" && !((watsonx || sapAI) && finishReason == "stop") {
		return Turn{}, chatFinishError(finishReason)
	}
	for index := 0; index < len(parts); index++ {
		call := parts[index]
		if call != nil && watsonx && strings.HasPrefix(model.ID, "ibm/granite") {
			var nested string
			if json.Unmarshal([]byte(call.Function.Arguments), &nested) == nil && json.Valid([]byte(nested)) {
				call.Function.Arguments = nested
			}
		}
		if call == nil || call.ID == "" || call.Function.Name == "" || !json.Valid([]byte(call.Function.Arguments)) {
			return Turn{}, errors.New("model returned incomplete tool arguments")
		}
		turn.Calls = append(turn.Calls, *call)
	}
	return turn, nil
}

func chatToolSchemas(model modelcfg.Model, schemas []map[string]any) []map[string]any {
	if model.ProviderID == "watsonx" || model.ProviderID == "sap-ai-core" || !chatSupportsStrictMode(model) {
		return schemas
	}
	result := make([]map[string]any, len(schemas))
	for index, schema := range schemas {
		function, ok := schema["function"].(map[string]any)
		if !ok || schema["type"] != "function" {
			result[index] = schema
			continue
		}
		copySchema := make(map[string]any, len(schema))
		for key, value := range schema {
			copySchema[key] = value
		}
		copyFunction := make(map[string]any, len(function)+1)
		for key, value := range function {
			copyFunction[key] = value
		}
		copyFunction["strict"] = false
		copySchema["function"] = copyFunction
		result[index] = copySchema
	}
	return result
}

func chatSupportsStrictMode(model modelcfg.Model) bool {
	providerID := strings.ToLower(model.ProviderID)
	baseURL := strings.ToLower(model.BaseURL)
	return providerID != "moonshotai" && providerID != "moonshotai-cn" && !strings.Contains(baseURL, "api.moonshot.") && providerID != "togetherai" && providerID != "together" && !strings.Contains(baseURL, "api.together.") && providerID != "cloudflare-ai-gateway" && !strings.Contains(baseURL, "gateway.ai.cloudflare.com") && providerID != "nvidia" && !strings.Contains(baseURL, "integrate.api.nvidia.com")
}

func chatSupportsStore(model modelcfg.Model) bool {
	providerID := strings.ToLower(model.ProviderID)
	baseURL := strings.ToLower(model.BaseURL)
	for _, blocked := range []struct{ id, host string }{
		{"nvidia", "integrate.api.nvidia.com"},
		{"cerebras", "cerebras.ai"},
		{"xai", "api.x.ai"},
		{"togetherai", "api.together."},
		{"chutes", "chutes.ai"},
		{"deepseek", "deepseek.com"},
		{"moonshotai", "api.moonshot."},
		{"opencode", "opencode.ai"},
		{"cloudflare-workers-ai", "api.cloudflare.com"},
		{"cloudflare-ai-gateway", "gateway.ai.cloudflare.com"},
		{"vercel-ai-gateway", "ai-gateway.vercel.sh"},
		{"ant-ling", "api.ant-ling.com"},
	} {
		if providerID == blocked.id || strings.Contains(baseURL, blocked.host) {
			return false
		}
	}
	if providerID == "zai" || providerID == "zai-coding-plan" || providerID == "zhipuai" || providerID == "zhipuai-coding-plan" || strings.Contains(baseURL, "api.z.ai") || strings.Contains(baseURL, "open.bigmodel.cn") || providerID == "moonshotai-cn" || providerID == "together" || strings.Contains(baseURL, "vercel.sh") {
		return false
	}
	return true
}

func chatFinishError(reason string) error {
	switch reason {
	case "length":
		return errors.New("model output reached its length limit")
	case "content_filter":
		return errors.New("model output was stopped by a content filter")
	case "error":
		return errors.New("model reported a generation error")
	case "network_error":
		return errors.New("model reported a network error")
	default:
		return errors.New("model stopped with unsupported finish reason")
	}
}

func zaiToolStreamModel(model modelcfg.Model) bool {
	if !zaiChatProvider(model.ProviderID) {
		return false
	}
	id := strings.ToLower(model.ID)
	return strings.HasPrefix(id, "glm-4.6") || strings.HasPrefix(id, "glm-4.7") || id == "glm-5" || strings.HasPrefix(id, "glm-5-") || strings.HasPrefix(id, "glm-5.")
}

func zaiChatProvider(id string) bool {
	switch id {
	case "zai", "zai-coding-plan", "zhipuai", "zhipuai-coding-plan":
		return true
	}
	return false
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

var sapAIPlaceholderEscaper = strings.NewReplacer("{{", "{\u200b{", "{%", "{\u200b%", "{#", "{\u200b#")

func sapAIMessages(messages []ChatMessage) ([]map[string]any, error) {
	out := make([]ChatMessage, len(messages))
	for index, message := range messages {
		message.Content = sapAIPlaceholderEscaper.Replace(message.Content)
		message.ReasoningDetails = nil
		message.ReasoningText = ""
		message.ReasoningField = ""
		if len(message.ToolCalls) > 0 {
			message.ToolCalls = append([]ToolCall(nil), message.ToolCalls...)
			for part := range message.ToolCalls {
				message.ToolCalls[part].Function.Arguments = sapAIPlaceholderEscaper.Replace(message.ToolCalls[part].Function.Arguments)
			}
		}
		out[index] = message
	}
	wire, err := chatMessagesWithReasoningDetails(out, "sap-ai-core")
	if err != nil {
		return nil, err
	}
	for index, message := range out {
		if message.Role == "assistant" && len(message.ToolCalls) > 0 && message.Content == "" {
			wire[index]["content"] = ""
		}
	}
	return wire, nil
}
