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
	"slices"
	"strings"

	"github.com/google/uuid"

	modelcfg "vista/internal/model"
)

// GeminiTurn uses the native generateContent protocol for text and function
// calls. Opaque thought signatures are returned with their function calls.
func GeminiTurn(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any) (Turn, error) {
	return geminiTurn(ctx, model, messages, schemas, nil)
}

// Validate explicit controls against published reasoning controls, independently
// of model naming. Unknown models may use explicit protocol options; automatic
// controls and summaries still require published capabilities.
func validateGeminiThinkingOptions(providerID, id, level string, budget *int) error {
	if level == "" && budget == nil {
		return nil
	}
	if budget != nil && (level != "" || *budget < -1 || *budget > 32768) {
		return errors.New("invalid Gemini thinking budget")
	}
	if level != "" && !slices.Contains([]string{"minimal", "low", "medium", "high"}, level) {
		return errors.New("invalid Gemini thinking level")
	}
	metadata, known := ompModelForMetadata(providerID, strings.TrimPrefix(id, "models/"))
	if known && !metadata.Reasoning {
		return errors.New("Gemini model does not support thinking")
	}
	if len(metadata.ReasoningOptions) > 0 {
		controls := controlsFor(modelcfg.Model{ProviderID: providerID, ID: id})
		if budget != nil && controls.budget == nil {
			return errors.New("Gemini model does not support thinking budgets")
		}
		if budget != nil && *budget >= 0 && !(controls.toggle && *budget == 0) && (controls.budget.Min != nil && *budget < *controls.budget.Min || controls.budget.Max != nil && *budget > *controls.budget.Max) {
			return errors.New("Gemini thinking budget is outside the published range")
		}
		if level != "" && !slices.Contains(controls.efforts, level) {
			return errors.New("Gemini model does not support thinking levels")
		}
	}
	return nil
}

func geminiTurn(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any, onText func(string) error) (Turn, error) {
	return geminiTurnWithReasoning(ctx, model, messages, schemas, onText, nil)
}

func geminiTurnWithReasoning(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any, onText, onReasoning func(string) error) (Turn, error) {
	if err := validateImageMessages(messages); err != nil {
		return Turn{}, err
	}
	if model.ID == "" || model.APIKey == "" && !model.Custom {
		return Turn{}, errors.New("Gemini model and API key are required")
	}
	base := model.BaseURL
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta"
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (model.Custom || isLoopback(u.Hostname())))) {
		return Turn{}, errors.New("invalid Gemini base URL: HTTPS or loopback HTTP required")
	}
	id := strings.TrimPrefix(model.ID, "models/")
	if id == "" || strings.ContainsAny(id, "/?#") {
		return Turn{}, errors.New("invalid Gemini model ID")
	}
	contents, system, err := geminiContents(messages)
	if err != nil {
		return Turn{}, err
	}
	declarations, err := geminiTools(schemas)
	if err != nil {
		return Turn{}, err
	}
	body := map[string]any{"contents": contents}
	if !model.ReasoningDisabled {
		if err := validateGeminiThinkingOptions(model.ProviderID, id, model.ThinkingLevel, model.ThinkingBudget); err != nil {
			return Turn{}, err
		}
		thinking := map[string]any{}
		metadata, _ := ompModelForMetadata(model.ProviderID, id)
		if metadata.Reasoning || model.ThinkingBudget != nil || model.ThinkingLevel != "" {
			thinking["includeThoughts"] = true
		}
		if model.ThinkingBudget != nil {
			thinking["thinkingBudget"] = *model.ThinkingBudget
		} else if model.ThinkingLevel != "" {
			thinking["thinkingLevel"] = strings.ToUpper(model.ThinkingLevel)
		}
		if len(thinking) > 0 {
			body["generationConfig"] = map[string]any{"thinkingConfig": thinking}
		}
	}
	if system != "" {
		body["systemInstruction"] = map[string]any{"parts": []any{map[string]any{"text": system}}}
	}
	if len(declarations) > 0 {
		body["tools"] = []any{map[string]any{"functionDeclarations": declarations}}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return Turn{}, err
	}
	endpoint := strings.TrimRight(u.String(), "/") + "/models/" + url.PathEscape(id) + ":streamGenerateContent?alt=sse"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return Turn{}, err
	}
	if model.Provider == "vertex-gemini" && !model.VertexAPIKey {
		request.Header.Set("Authorization", "Bearer "+model.APIKey)
	} else if model.APIKey != "" {
		request.Header.Set("x-goog-api-key", model.APIKey)
	}
	aihubmixHeader(request, model)
	request.Header.Set("Content-Type", "application/json")
	response, err := doModelRequest(request, model)
	if err != nil {
		return Turn{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Turn{}, modelHTTPError("Gemini", response)
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		return Turn{}, modelHTTPError("Gemini (unexpected content type)", response)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), maxEventBytes)
	var payload strings.Builder
	sawData := false
	var text strings.Builder
	var calls []ToolCall
	var toolBytes toolCollectionBytes
	reasoningBytes := 0
	pendingSignature := ""
	finished := false
	finishReason := ""
	var usage *ModelUsage
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			if fragment, ok := sseDataLine(line); ok {
				separator := 0
				if sawData {
					separator = 1
				}
				if payload.Len()+len(fragment)+separator > maxEventBytes {
					return Turn{}, errors.New("Gemini stream event exceeds 128 KiB")
				}
				if sawData {
					payload.WriteByte('\n')
				}
				payload.WriteString(fragment)
				sawData = true
			}
			continue
		}
		sawData = false
		if payload.Len() == 0 {
			continue
		}
		raw := payload.String()
		payload.Reset()
		if sseKeepalive(raw) {
			continue
		}
		if raw == "[DONE]" {
			break
		}
		var chunk struct {
			Error         json.RawMessage `json:"error"`
			UsageMetadata *struct {
				PromptTokens    int64 `json:"promptTokenCount"`
				CandidateTokens int64 `json:"candidatesTokenCount"`
				CachedTokens    int64 `json:"cachedContentTokenCount"`
				ThoughtTokens   int64 `json:"thoughtsTokenCount"`
			} `json:"usageMetadata"`
			PromptFeedback struct {
				BlockReason string `json:"blockReason"`
			} `json:"promptFeedback"`
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text             string `json:"text"`
						Thought          bool   `json:"thought"`
						ThoughtSignature string `json:"thoughtSignature"`
						FunctionCall     *struct {
							ID   string          `json:"id"`
							Name string          `json:"name"`
							Args json.RawMessage `json:"args"`
						} `json:"functionCall"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
		}
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			return Turn{}, fmt.Errorf("invalid Gemini stream event: %w", err)
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return Turn{}, modelStreamError("Gemini", chunk.Error)
		}
		if chunk.PromptFeedback.BlockReason != "" {
			switch chunk.PromptFeedback.BlockReason {
			case "SAFETY", "BLOCKLIST", "PROHIBITED_CONTENT", "IMAGE_SAFETY", "JAILBREAK", "OTHER":
				return Turn{}, fmt.Errorf("Gemini blocked prompt: %s", chunk.PromptFeedback.BlockReason)
			}
			return Turn{}, errors.New("Gemini blocked prompt")
		}
		if chunk.UsageMetadata != nil {
			u := chunk.UsageMetadata
			if !validReportedTokens(u.PromptTokens) || !validReportedTokens(u.CandidateTokens) || !validReportedTokens(u.CachedTokens) || !validReportedTokens(u.ThoughtTokens) {
				return Turn{}, errors.New("Gemini reported invalid token usage")
			}
			candidate := ModelUsage{InputTokens: u.PromptTokens, OutputTokens: u.CandidateTokens + u.ThoughtTokens, CacheReadTokens: u.CachedTokens, ReasoningTokens: u.ThoughtTokens}
			if !validModelUsage(candidate) || candidate.CacheReadTokens > candidate.InputTokens {
				return Turn{}, errors.New("Gemini reported invalid token usage")
			}
			usage = &candidate
		}
		if len(chunk.Candidates) > 1 {
			return Turn{}, errors.New("Gemini returned multiple candidates")
		}
		for _, candidate := range chunk.Candidates {
			for _, part := range candidate.Content.Parts {
				if part.Thought {
					if part.FunctionCall != nil || len(part.Text) > maxReplyBytes-reasoningBytes {
						return Turn{}, errors.New("Gemini reasoning exceeds limits")
					}
					if part.Text != "" && onReasoning == nil {
						return Turn{}, errors.New("Gemini reasoning stream requires a sink")
					}
					reasoningBytes += len(part.Text)
					if onReasoning != nil && part.Text != "" {
						if err := onReasoning(part.Text); err != nil {
							return Turn{}, err
						}
					}
					if len(part.ThoughtSignature) > 8192 {
						return Turn{}, errors.New("Gemini thought signature exceeds limits")
					}
					if part.ThoughtSignature != "" {
						pendingSignature = part.ThoughtSignature
					}
					continue
				}
				if text.Len()+len(part.Text) > maxReplyBytes {
					return Turn{}, errors.New("Gemini reply exceeds 256 KiB")
				}
				text.WriteString(part.Text)
				if onText != nil && part.Text != "" {
					if err := onText(part.Text); err != nil {
						return Turn{}, err
					}
				}
				if part.FunctionCall == nil {
					continue
				}
				if part.FunctionCall.Name == "" || len(part.FunctionCall.Name) > 128 || len(part.ThoughtSignature) > 8192 {
					return Turn{}, errors.New("Gemini function call exceeds limits")
				}
				arguments := part.FunctionCall.Args
				if len(arguments) == 0 {
					arguments = []byte("{}")
				}
				var object map[string]any
				if len(arguments) > maxToolArgsBytes || json.Unmarshal(arguments, &object) != nil || object == nil {
					return Turn{}, errors.New("Gemini function arguments are invalid or incomplete")
				}
				id := part.FunctionCall.ID
				if id == "" {
					id = "gemini-call-" + uuid.NewString()
				}
				if len(id) > 128 {
					return Turn{}, errors.New("Gemini function call ID exceeds limits")
				}
				signature := part.ThoughtSignature
				if signature == "" {
					signature = pendingSignature
				}
				pendingSignature = ""
				var call ToolCall
				call.ID, call.Type, call.Function.Name, call.Function.Arguments, call.ThoughtSignature, call.GeminiID = id, "function", part.FunctionCall.Name, string(arguments), signature, part.FunctionCall.ID
				if err := toolBytes.set(fmt.Sprint(len(calls)), len(id)+len(call.Function.Name)+len(arguments)+len(signature)+len(call.GeminiID)); err != nil {
					return Turn{}, err
				}
				calls = append(calls, call)
			}
			if candidate.FinishReason != "" {
				if candidate.FinishReason != "STOP" {
					switch candidate.FinishReason {
					case "SAFETY", "RECITATION", "MAX_TOKENS", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "OTHER", "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL", "MODEL_ARMOR", "IMAGE_SAFETY":
						return Turn{}, fmt.Errorf("Gemini stopped with reason %s", candidate.FinishReason)
					}
					return Turn{}, errors.New("Gemini stopped with unsupported reason")
				}
				finished = true
				finishReason = candidate.FinishReason
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Turn{}, fmt.Errorf("Gemini stream read: %w", err)
	}
	if payload.Len() != 0 || !finished {
		return Turn{}, errors.New("Gemini stream ended without a complete response")
	}
	return Turn{Text: text.String(), Calls: calls, FinishReason: finishReason, Usage: usage}, nil
}

func geminiContents(messages []ChatMessage) ([]map[string]any, string, error) {
	contents := make([]map[string]any, 0, len(messages))
	var system strings.Builder
	type functionIdentity struct{ name, id string }
	names := map[string]functionIdentity{}
	for _, message := range messages {
		images, err := imagePayloads(message)
		if err != nil {
			return nil, "", err
		}
		switch message.Role {
		case "system":
			if system.Len() > 0 {
				system.WriteByte('\n')
			}
			system.WriteString(message.Content)
		case "user":
			parts := []any{}
			if message.Content != "" || len(images) == 0 {
				parts = append(parts, map[string]any{"text": message.Content})
			}
			for i, url := range images {
				parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": message.Images[i].MediaType, "data": imageBase64(url)}})
			}
			contents = append(contents, map[string]any{"role": "user", "parts": parts})
		case "assistant":
			parts := []any{}
			if message.Content != "" {
				parts = append(parts, map[string]any{"text": message.Content})
			}
			for _, call := range message.ToolCalls {
				var args map[string]any
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil || args == nil {
					return nil, "", errors.New("invalid previous Gemini function arguments")
				}
				functionCall := map[string]any{"name": call.Function.Name, "args": args}
				if call.GeminiID != "" {
					functionCall["id"] = call.GeminiID
				}
				part := map[string]any{"functionCall": functionCall}
				if call.ThoughtSignature != "" {
					part["thoughtSignature"] = call.ThoughtSignature
				}
				parts = append(parts, part)
				names[call.ID] = functionIdentity{call.Function.Name, call.GeminiID}
			}
			if len(parts) > 0 {
				contents = append(contents, map[string]any{"role": "model", "parts": parts})
			}
		case "tool":
			identity := names[message.ToolCallID]
			if identity.name == "" {
				return nil, "", errors.New("Gemini tool result lacks a matching function call")
			}
			functionResponse := map[string]any{"name": identity.name, "response": map[string]any{"result": message.Content}}
			if identity.id != "" {
				functionResponse["id"] = identity.id
			}
			part := map[string]any{"functionResponse": functionResponse}
			if len(contents) > 0 && contents[len(contents)-1]["role"] == "user" {
				contents[len(contents)-1]["parts"] = append(contents[len(contents)-1]["parts"].([]any), part)
			} else {
				contents = append(contents, map[string]any{"role": "user", "parts": []any{part}})
			}
		default:
			return nil, "", errors.New("invalid Gemini message role")
		}
	}
	return contents, system.String(), nil
}

func geminiTools(schemas []map[string]any) ([]map[string]any, error) {
	declarations := make([]map[string]any, 0, len(schemas))
	for _, schema := range schemas {
		if schema["type"] != "function" {
			return nil, errors.New("Gemini supports only function tool schemas")
		}
		function, ok := schema["function"].(map[string]any)
		if !ok {
			return nil, errors.New("invalid Gemini function schema")
		}
		parameters, err := geminiSchema(function["parameters"])
		if err != nil {
			return nil, err
		}
		declarations = append(declarations, map[string]any{"name": function["name"], "description": function["description"], "parameters": parameters})
	}
	return declarations, nil
}

func geminiSchema(value any) (map[string]any, error) {
	source, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("invalid Gemini function parameter schema")
	}
	result := map[string]any{}
	for _, key := range []string{"description", "enum", "required", "format", "minimum", "maximum", "minItems", "maxItems", "nullable"} {
		if item, ok := source[key]; ok {
			result[key] = item
		}
	}
	kind, ok := source["type"].(string)
	if !ok {
		return nil, errors.New("Gemini function parameter type is required")
	}
	switch strings.ToUpper(kind) {
	case "OBJECT", "ARRAY", "STRING", "NUMBER", "INTEGER", "BOOLEAN":
		result["type"] = strings.ToUpper(kind)
	default:
		return nil, errors.New("unsupported Gemini function parameter type")
	}
	if properties, ok := source["properties"]; ok {
		items, ok := properties.(map[string]any)
		if !ok {
			return nil, errors.New("invalid Gemini function properties")
		}
		converted := make(map[string]any, len(items))
		for name, value := range items {
			child, err := geminiSchema(value)
			if err != nil {
				return nil, err
			}
			converted[name] = child
		}
		result["properties"] = converted
	}
	if item, ok := source["items"]; ok {
		child, err := geminiSchema(item)
		if err != nil {
			return nil, err
		}
		result["items"] = child
	}
	return result, nil
}
