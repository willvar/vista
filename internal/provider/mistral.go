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
	"strconv"
	"strings"

	modelcfg "vista/internal/model"
)

func validMistralToolID(id string) bool {
	if len(id) != 9 {
		return false
	}
	for _, ch := range id {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9') {
			return false
		}
	}
	return true
}

func mistralToolIDHash(id string) string {
	hash := func(seed uint32) string {
		value := seed
		for _, ch := range id {
			// JavaScript iterates code points but charCodeAt(0) hashes the first UTF-16 unit.
			if ch > 0xffff {
				ch = 0xd800 + (ch-0x10000)/0x400
			}
			value = (value ^ uint32(ch)) * 16777619
		}
		result := strconv.FormatUint(uint64(value), 36)
		return strings.Repeat("0", max(0, 7-len(result))) + result
	}
	joined := hash(2166136261) + hash(2246822519)
	return joined[len(joined)-9:]
}

func mistralToolIDNormalizer(ids []string) func(string) string {
	used := map[string]bool{}
	for _, id := range ids {
		if validMistralToolID(id) {
			used[id] = true
		}
	}
	normalized := map[string]string{}
	return func(id string) string {
		if validMistralToolID(id) {
			return id
		}
		if previous := normalized[id]; previous != "" {
			return previous
		}
		candidate := mistralToolIDHash(id)
		for attempt := 1; used[candidate]; attempt++ {
			candidate = mistralToolIDHash(fmt.Sprintf("%s:%d", id, attempt))
		}
		used[candidate] = true
		normalized[id] = candidate
		return candidate
	}
}

func mistralMessages(messages []ChatMessage) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(messages))
	names := map[string]string{}
	var ids []string
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			ids = append(ids, call.ID)
		}
		if message.Role == "tool" {
			ids = append(ids, message.ToolCallID)
		}
	}
	normalize := mistralToolIDNormalizer(ids)
	for _, message := range messages {
		images, err := imagePayloads(message)
		if err != nil {
			return nil, err
		}
		switch message.Role {
		case "system", "user":
			if len(images) == 0 {
				out = append(out, map[string]any{"role": message.Role, "content": message.Content})
				break
			}
			parts := make([]map[string]any, 0, len(images)+1)
			if message.Content != "" {
				parts = append(parts, map[string]any{"type": "text", "text": message.Content})
			}
			for _, dataURL := range images {
				parts = append(parts, map[string]any{"type": "image_url", "image_url": dataURL})
			}
			out = append(out, map[string]any{"role": message.Role, "content": parts})
		case "assistant":
			item := map[string]any{"role": "assistant", "content": message.Content}
			if len(message.MistralParts) > 0 {
				parts := make([]map[string]any, 0, len(message.MistralParts))
				for _, part := range message.MistralParts {
					switch part.Type {
					case "text":
						parts = append(parts, map[string]any{"type": "text", "text": part.Text})
					case "thinking":
						parts = append(parts, map[string]any{"type": "thinking", "thinking": []map[string]string{{"type": "text", "text": part.Text}}, "closed": true})
					default:
						return nil, errors.New("invalid previous Mistral content part")
					}
				}
				item["content"] = parts
			}
			if len(message.ToolCalls) > 0 {
				calls := make([]map[string]any, 0, len(message.ToolCalls))
				for _, call := range message.ToolCalls {
					if call.ID == "" || call.Function.Name == "" || !json.Valid([]byte(call.Function.Arguments)) {
						return nil, errors.New("invalid previous Mistral tool call")
					}
					id := normalize(call.ID)
					names[id] = call.Function.Name
					calls = append(calls, map[string]any{"id": id, "type": "function", "function": map[string]any{"name": call.Function.Name, "arguments": call.Function.Arguments}})
				}
				item["tool_calls"] = calls
			}
			out = append(out, item)
		case "tool":
			id := normalize(message.ToolCallID)
			name := names[id]
			if name == "" {
				return nil, errors.New("Mistral tool result has no preceding named call")
			}
			out = append(out, map[string]any{"role": "tool", "tool_call_id": id, "name": name, "content": message.Content})
		default:
			return nil, errors.New("invalid Mistral message role")
		}
	}
	return out, nil
}

func mistralTools(schemas []map[string]any) ([]map[string]any, error) {
	tools := make([]map[string]any, 0, len(schemas))
	for _, schema := range schemas {
		function, ok := schema["function"].(map[string]any)
		if schema["type"] != "function" || !ok || function["name"] == "" || function["parameters"] == nil {
			return nil, errors.New("invalid Mistral function schema")
		}
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": function["name"], "description": function["description"], "parameters": function["parameters"], "strict": false}})
	}
	return tools, nil
}

func MistralTurn(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any) (Turn, error) {
	return mistralTurn(ctx, model, messages, schemas, nil)
}

func mistralTurn(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any, onText func(string) error) (Turn, error) {
	return mistralTurnWithReasoning(ctx, model, messages, schemas, onText, nil)
}

func mistralTurnWithReasoning(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any, onText, onReasoning func(string) error) (Turn, error) {
	if err := validateImageMessages(messages); err != nil {
		return Turn{}, err
	}
	if model.ID == "" || model.APIKey == "" {
		return Turn{}, errors.New("Mistral model and API key are required")
	}
	base := model.BaseURL
	if base == "" {
		base = "https://api.mistral.ai/v1"
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) {
		return Turn{}, errors.New("invalid Mistral base URL: HTTPS or loopback HTTP required")
	}
	converted, err := mistralMessages(messages)
	if err != nil {
		return Turn{}, err
	}
	tools, err := mistralTools(schemas)
	if err != nil {
		return Turn{}, err
	}
	body := map[string]any{"model": model.ID, "stream": true, "messages": converted}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return Turn{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(u.String(), "/")+"/chat/completions", bytes.NewReader(encoded))
	if err != nil {
		return Turn{}, err
	}
	request.Header.Set("Authorization", "Bearer "+model.APIKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := doModelRequest(request, model)
	if err != nil {
		return Turn{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Turn{}, modelHTTPError("Mistral", response)
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		return Turn{}, modelHTTPError("Mistral (unexpected content type)", response)
	}
	return parseMistralStreamWithReasoning(response.Body, onText, onReasoning)
}

type mistralCall struct {
	id, name string
	args     strings.Builder
}

func parseMistralStreamWithReasoning(body io.Reader, onText, onReasoning func(string) error) (Turn, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), maxEventBytes)
	var payload strings.Builder
	sawData := false
	var answer strings.Builder
	reasoningBytes := 0
	type partBuilder struct {
		kind string
		text strings.Builder
	}
	var parts []*partBuilder
	appendPart := func(kind, fragment string) error {
		if fragment == "" {
			return nil
		}
		if len(parts) == 0 || parts[len(parts)-1].kind != kind {
			if len(parts) >= 256 {
				return errors.New("Mistral content parts exceed limits")
			}
			parts = append(parts, &partBuilder{kind: kind})
		}
		parts[len(parts)-1].text.WriteString(fragment)
		return nil
	}
	calls := map[int]*mistralCall{}
	var toolBytes toolCollectionBytes
	latest := -1
	finish := ""
	done := false
	var usage *ModelUsage
	consume := func(raw string) error {
		if raw == "[DONE]" {
			done = true
			return nil
		}
		var event struct {
			Error json.RawMessage `json:"error"`
			Usage *struct {
				PromptTokens        int64 `json:"prompt_tokens"`
				CompletionTokens    int64 `json:"completion_tokens"`
				PromptTokensDetails struct {
					CachedTokens int64 `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
				CompletionTokensDetail struct {
					ReasoningTokens int64 `json:"reasoning_tokens"`
				} `json:"completion_tokens_details"`
			} `json:"usage"`
			Choices []struct {
				Delta struct {
					Content   json.RawMessage `json:"content"`
					ToolCalls []struct {
						Index    *int   `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string          `json:"name"`
							Arguments json.RawMessage `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return fmt.Errorf("invalid Mistral stream event: %w", err)
		}
		if len(event.Error) > 0 && string(event.Error) != "null" {
			return modelStreamError("Mistral", event.Error)
		}
		if event.Usage != nil {
			u := event.Usage
			candidate := ModelUsage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens, CacheReadTokens: u.PromptTokensDetails.CachedTokens, ReasoningTokens: u.CompletionTokensDetail.ReasoningTokens}
			if !validModelUsage(candidate) || candidate.CacheReadTokens > candidate.InputTokens || candidate.ReasoningTokens > candidate.OutputTokens {
				return errors.New("Mistral reported invalid token usage")
			}
			usage = &candidate
		}
		if len(event.Choices) > 1 {
			return errors.New("Mistral returned multiple choices")
		}
		for _, choice := range event.Choices {
			if finish != "" && (len(choice.Delta.Content) > 0 && string(choice.Delta.Content) != "null" || len(choice.Delta.ToolCalls) > 0) {
				return errors.New("Mistral returned content after finish reason")
			}
			type segment struct {
				text     string
				thinking bool
			}
			var segments []segment
			content := choice.Delta.Content
			if len(content) > 0 && string(content) != "null" {
				if content[0] == '"' {
					var fragment string
					if err := json.Unmarshal(content, &fragment); err != nil {
						return errors.New("invalid Mistral text content")
					}
					segments = append(segments, segment{text: fragment})
				} else {
					var parts []struct {
						Type     string          `json:"type"`
						Text     string          `json:"text"`
						Thinking json.RawMessage `json:"thinking"`
					}
					if err := json.Unmarshal(content, &parts); err != nil {
						return errors.New("unsupported Mistral content shape")
					}
					for _, part := range parts {
						switch part.Type {
						case "text":
							if len(part.Thinking) > 0 && string(part.Thinking) != "null" {
								return errors.New("ambiguous Mistral text content")
							}
							segments = append(segments, segment{text: part.Text})
						case "thinking":
							if len(part.Thinking) == 0 || string(part.Thinking) == "null" {
								if part.Text == "" {
									return errors.New("invalid Mistral thinking content")
								}
								segments = append(segments, segment{text: part.Text, thinking: true})
								continue
							}
							if part.Text != "" {
								return errors.New("ambiguous Mistral thinking content")
							}
							var text string
							if err := json.Unmarshal(part.Thinking, &text); err == nil {
								segments = append(segments, segment{text: text, thinking: true})
								continue
							}
							var thoughts []struct {
								Type string `json:"type"`
								Text string `json:"text"`
							}
							if json.Unmarshal(part.Thinking, &thoughts) != nil || len(thoughts) == 0 {
								return errors.New("invalid Mistral thinking content")
							}
							for _, thought := range thoughts {
								if thought.Type != "" && thought.Type != "text" {
									return errors.New("unsupported Mistral thinking type")
								}
								segments = append(segments, segment{text: thought.Text, thinking: true})
							}
						default:
							return errors.New("Mistral content type is not yet supported")
						}
					}
				}
			}
			for _, part := range segments {
				fragment := part.text
				if part.thinking {
					if len(fragment) > maxReplyBytes-reasoningBytes {
						return errors.New("Mistral reasoning exceeds 256 KiB")
					}
					if fragment != "" && onReasoning == nil {
						return errors.New("Mistral reasoning stream requires a sink")
					}
					reasoningBytes += len(fragment)
					if err := appendPart("thinking", fragment); err != nil {
						return err
					}
					if onReasoning != nil && fragment != "" {
						if err := onReasoning(fragment); err != nil {
							return err
						}
					}
					continue
				}
				if answer.Len()+len(fragment) > maxReplyBytes {
					return errors.New("Mistral reply exceeds 256 KiB")
				}
				answer.WriteString(fragment)
				if err := appendPart("text", fragment); err != nil {
					return err
				}
				if onText != nil && fragment != "" {
					if err := onText(fragment); err != nil {
						return err
					}
				}
			}
			for position, delta := range choice.Delta.ToolCalls {
				index := latest
				if delta.Index != nil {
					index = *delta.Index
				} else if len(choice.Delta.ToolCalls) > 1 {
					index = position
				} else if index < 0 {
					index = 0
				}
				if index < 0 {
					return errors.New("Mistral tool call index is negative")
				}
				latest = index
				call := calls[index]
				if call == nil {
					call = &mistralCall{}
					calls[index] = call
				}
				if len(call.id)+len(delta.ID) > 256 || len(call.name)+len(delta.Function.Name) > 128 {
					return errors.New("Mistral tool call exceeds limits")
				}
				if delta.ID != "" {
					call.id += delta.ID
				}
				call.name += delta.Function.Name
				if len(delta.Function.Arguments) > 0 && string(delta.Function.Arguments) != "null" {
					if delta.Function.Arguments[0] == '"' {
						var fragment string
						if err := json.Unmarshal(delta.Function.Arguments, &fragment); err != nil {
							return errors.New("invalid Mistral tool argument fragment")
						}
						if call.args.Len()+len(fragment) > maxToolArgsBytes {
							return errors.New("Mistral tool call exceeds limits")
						}
						call.args.WriteString(fragment)
					} else if call.args.Len() == 0 && json.Valid(delta.Function.Arguments) {
						if len(delta.Function.Arguments) > maxToolArgsBytes {
							return errors.New("Mistral tool call exceeds limits")
						}
						call.args.Write(delta.Function.Arguments)
					} else {
						return errors.New("invalid Mistral tool arguments")
					}
				}
				if err := toolBytes.set(strconv.Itoa(index), len(call.id)+len(call.name)+call.args.Len()); err != nil {
					return err
				}
			}
			if choice.FinishReason != nil {
				finish = *choice.FinishReason
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			sawData = false
			if payload.Len() == 0 {
				continue
			}
			raw := payload.String()
			payload.Reset()
			if sseKeepalive(raw) {
				continue
			}
			if err := consume(raw); err != nil {
				return Turn{}, err
			}
			if done {
				break
			}
			continue
		}
		if fragment, ok := sseDataLine(line); ok {
			separator := 0
			if sawData {
				separator = 1
			}
			if payload.Len()+len(fragment)+separator > maxEventBytes {
				return Turn{}, errors.New("Mistral stream event exceeds 128 KiB")
			}
			if sawData {
				payload.WriteByte('\n')
			}
			payload.WriteString(fragment)
			sawData = true
		}
	}
	if err := scanner.Err(); err != nil {
		return Turn{}, fmt.Errorf("Mistral stream read: %w", err)
	}
	if !done || (finish != "stop" && finish != "tool_calls") {
		return Turn{}, errors.New("Mistral stream ended without a complete response")
	}
	turn := Turn{Text: answer.String(), FinishReason: finish, Usage: usage}
	if reasoningBytes > 0 {
		turn.MistralParts = make([]MistralPart, 0, len(parts))
		for _, part := range parts {
			turn.MistralParts = append(turn.MistralParts, MistralPart{Type: part.kind, Text: part.text.String()})
		}
	}
	if len(calls) == 0 {
		if finish != "stop" {
			return Turn{}, errors.New("Mistral reported tool calls without a call")
		}
		return turn, nil
	}
	ids := make([]string, 0, len(calls))
	for index := 0; index < len(calls); index++ {
		if calls[index] == nil {
			return Turn{}, errors.New("Mistral returned incomplete tool call")
		}
		id := calls[index].id
		if id == "" {
			id = fmt.Sprintf("generated-%d", index)
		}
		ids = append(ids, id)
	}
	normalize := mistralToolIDNormalizer(ids)
	usedIDs := map[string]bool{}
	for index := 0; index < len(calls); index++ {
		call := calls[index]
		if call == nil || call.name == "" || call.args.Len() == 0 {
			return Turn{}, errors.New("Mistral returned incomplete tool call")
		}
		var object map[string]any
		if json.Unmarshal([]byte(call.args.String()), &object) != nil || object == nil {
			return Turn{}, errors.New("Mistral returned invalid tool arguments")
		}
		var tool ToolCall
		tool.ID, tool.Type, tool.Function.Name, tool.Function.Arguments = normalize(ids[index]), "function", call.name, call.args.String()
		if usedIDs[tool.ID] {
			return Turn{}, errors.New("Mistral returned duplicate tool call IDs")
		}
		usedIDs[tool.ID] = true
		turn.Calls = append(turn.Calls, tool)
	}
	return turn, nil
}
