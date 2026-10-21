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

// Cohere's pinned AI SDK adapter uses the native v2 /chat stream.
func cohereTurnWithReasoning(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any, onText, onReasoning func(string) error) (Turn, error) {
	if err := validateImageMessages(messages); err != nil {
		return Turn{}, err
	}
	if model.ID == "" || model.APIKey == "" && !model.Custom {
		return Turn{}, errors.New("Cohere model and API key are required")
	}
	base := model.BaseURL
	if base == "" {
		base = "https://api.cohere.com/v2"
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (model.Custom || isLoopback(u.Hostname())))) {
		return Turn{}, errors.New("invalid Cohere base URL: HTTPS or loopback HTTP required")
	}
	prompt := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		images, err := imagePayloads(message)
		if err != nil {
			return Turn{}, err
		}
		switch message.Role {
		case "system", "user":
			if len(images) == 0 {
				prompt = append(prompt, map[string]any{"role": message.Role, "content": message.Content})
				break
			}
			parts := make([]map[string]any, 0, len(images)+1)
			if message.Content != "" {
				parts = append(parts, map[string]any{"type": "text", "text": message.Content})
			}
			for _, dataURL := range images {
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": dataURL}})
			}
			prompt = append(prompt, map[string]any{"role": message.Role, "content": parts})
		case "assistant":
			item := map[string]any{"role": "assistant"}
			if len(message.ToolCalls) == 0 {
				item["content"] = message.Content
			} else {
				calls := make([]map[string]any, 0, len(message.ToolCalls))
				for _, call := range message.ToolCalls {
					if call.ID == "" || call.Function.Name == "" || len(call.Function.Arguments) > maxToolArgsBytes || !validToolObject(call.Function.Arguments) {
						return Turn{}, errors.New("invalid Cohere previous tool call")
					}
					calls = append(calls, map[string]any{"id": call.ID, "type": "function", "function": map[string]any{"name": call.Function.Name, "arguments": call.Function.Arguments}})
				}
				item["tool_calls"] = calls
			}
			prompt = append(prompt, item)
		case "tool":
			if message.ToolCallID == "" {
				return Turn{}, errors.New("Cohere tool result lacks a call ID")
			}
			prompt = append(prompt, map[string]any{"role": "tool", "content": message.Content, "tool_call_id": message.ToolCallID})
		default:
			return Turn{}, errors.New("invalid Cohere message role")
		}
	}
	tools := make([]map[string]any, 0, len(schemas))
	for _, schema := range schemas {
		function, ok := schema["function"].(map[string]any)
		if schema["type"] != "function" || !ok || function["name"] == "" {
			return Turn{}, errors.New("invalid Cohere function schema")
		}
		parameters, ok := function["parameters"].(map[string]any)
		if !ok {
			return Turn{}, errors.New("Cohere tool parameters must be a JSON schema object")
		}
		entry := map[string]any{"name": function["name"], "parameters": parameters}
		if description, ok := function["description"].(string); ok && description != "" {
			entry["description"] = description
		}
		tools = append(tools, map[string]any{"type": "function", "function": entry})
	}
	body := map[string]any{"model": model.ID, "messages": prompt, "stream": true}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return Turn{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(u.String(), "/")+"/chat", bytes.NewReader(encoded))
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
		return Turn{}, modelHTTPError("Cohere", response)
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		return Turn{}, errors.New("unexpected Cohere content type")
	}
	return parseCohereStreamWithReasoning(response.Body, onText, onReasoning)
}

func validToolObject(value string) bool {
	var object map[string]any
	return json.Unmarshal([]byte(value), &object) == nil && object != nil
}

func parseCohereStream(body io.Reader, onText func(string) error) (Turn, error) {
	return parseCohereStreamWithReasoning(body, onText, nil)
}

func parseCohereStreamWithReasoning(body io.Reader, onText, onReasoning func(string) error) (Turn, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), maxEventBytes)
	var payload strings.Builder
	sawData := false
	var reply strings.Builder
	reasoningBytes := 0
	var calls []ToolCall
	var toolBytes toolCollectionBytes
	var pending *ToolCall
	finished := false
	finishReason := ""
	var usage *ModelUsage
	consume := func(data string) error {
		var event struct {
			Type  string `json:"type"`
			Delta struct {
				Message struct {
					Content struct {
						Type     string          `json:"type"`
						Text     string          `json:"text"`
						Thinking json.RawMessage `json:"thinking"`
					} `json:"content"`
					ToolCalls struct {
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
				Usage        *struct {
					Tokens *struct {
						InputTokens  int64 `json:"input_tokens"`
						OutputTokens int64 `json:"output_tokens"`
					} `json:"tokens"`
					BilledUnits *struct {
						InputTokens  int64 `json:"input_tokens"`
						OutputTokens int64 `json:"output_tokens"`
					} `json:"billed_units"`
				} `json:"usage"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return fmt.Errorf("invalid Cohere stream event: %w", err)
		}
		if finished {
			return errors.New("Cohere stream continued after finish")
		}
		switch event.Type {
		case "content-delta":
			if len(event.Delta.Message.Content.Thinking) != 0 && string(event.Delta.Message.Content.Thinking) != "null" {
				var delta string
				if err := json.Unmarshal(event.Delta.Message.Content.Thinking, &delta); err != nil {
					var block struct {
						Text string `json:"text"`
					}
					if json.Unmarshal(event.Delta.Message.Content.Thinking, &block) != nil || block.Text == "" {
						return errors.New("invalid Cohere reasoning content")
					}
					delta = block.Text
				}
				if len(delta) > maxReplyBytes-reasoningBytes {
					return errors.New("Cohere reasoning exceeds 256 KiB")
				}
				if delta != "" && onReasoning == nil {
					return errors.New("Cohere reasoning stream requires a sink")
				}
				reasoningBytes += len(delta)
				if onReasoning != nil && delta != "" {
					if err := onReasoning(delta); err != nil {
						return err
					}
				}
				return nil
			}
			delta := event.Delta.Message.Content.Text
			if reply.Len()+len(delta) > maxReplyBytes {
				return errors.New("Cohere text exceeds 256 KiB")
			}
			reply.WriteString(delta)
			if onText != nil && delta != "" {
				return onText(delta)
			}
		case "tool-call-start":
			item := event.Delta.Message.ToolCalls
			if pending != nil || item.ID == "" || len(item.ID) > 256 || item.Function.Name == "" || len(item.Function.Name) > 128 || len(item.Function.Arguments) > maxToolArgsBytes {
				return errors.New("invalid Cohere tool call start")
			}
			for _, call := range calls {
				if call.ID == item.ID {
					return errors.New("duplicate Cohere tool call ID")
				}
			}
			pending = &ToolCall{ID: item.ID, Type: "function"}
			pending.Function.Name = item.Function.Name
			pending.Function.Arguments = item.Function.Arguments
			if err := toolBytes.set(pending.ID, len(pending.Function.Name)+len(pending.Function.Arguments)); err != nil {
				return err
			}
		case "tool-call-delta":
			if pending == nil || len(pending.Function.Arguments)+len(event.Delta.Message.ToolCalls.Function.Arguments) > maxToolArgsBytes {
				return errors.New("Cohere tool arguments exceed limits")
			}
			pending.Function.Arguments += event.Delta.Message.ToolCalls.Function.Arguments
			if err := toolBytes.set(pending.ID, len(pending.Function.Name)+len(pending.Function.Arguments)); err != nil {
				return err
			}
		case "tool-call-end":
			if pending == nil {
				return errors.New("Cohere tool call ended without start")
			}
			if strings.TrimSpace(pending.Function.Arguments) == "" || strings.TrimSpace(pending.Function.Arguments) == "null" {
				pending.Function.Arguments = "{}"
			}
			if !validToolObject(pending.Function.Arguments) {
				return errors.New("invalid Cohere tool arguments")
			}
			if err := toolBytes.set(pending.ID, len(pending.Function.Name)+len(pending.Function.Arguments)); err != nil {
				return err
			}
			calls = append(calls, *pending)
			pending = nil
		case "message-end":
			if pending != nil || event.Delta.FinishReason != "COMPLETE" && event.Delta.FinishReason != "STOP_SEQUENCE" && event.Delta.FinishReason != "TOOL_CALL" || event.Delta.FinishReason == "TOOL_CALL" && len(calls) == 0 {
				return errors.New("Cohere stream ended with incomplete output")
			}
			if event.Delta.Usage != nil {
				reported := event.Delta.Usage.Tokens
				if reported != nil {
					candidate := ModelUsage{InputTokens: reported.InputTokens, OutputTokens: reported.OutputTokens}
					if !validModelUsage(candidate) {
						return errors.New("Cohere reported invalid token usage")
					}
					usage = &candidate
				}
				if billed := event.Delta.Usage.BilledUnits; billed != nil && (!validReportedTokens(billed.InputTokens) || !validReportedTokens(billed.OutputTokens)) {
					return errors.New("Cohere reported invalid billed token usage")
				}
			}
			finishReason = event.Delta.FinishReason
			finished = true
		case "content-start":
			if event.Delta.Message.Content.Type == "thinking" && onReasoning == nil {
				return errors.New("Cohere reasoning stream is not yet supported")
			}
		case "message-start", "content-end", "tool-plan-delta", "citation-start", "citation-end":
		case "error":
			return modelStreamError("Cohere", []byte(data))
		default:
			return errors.New("unknown Cohere event")
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
				return Turn{}, errors.New("Cohere event exceeds 128 KiB")
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
		return Turn{}, errors.New("Cohere stream ended without message-end")
	}
	return Turn{Text: reply.String(), Calls: calls, FinishReason: finishReason, Usage: usage}, nil
}
