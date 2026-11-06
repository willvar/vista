package provider

import (
	"encoding/json"
	"errors"
)

var responseHostedTools = map[string]string{
	"file_search_call":      "file_search",
	"code_interpreter_call": "code_interpreter",
	"computer_call":         "computer_use",
	"mcp_call":              "mcp",
}

func responseHostedCalls(items []json.RawMessage) ([]HostedCall, error) {
	var calls []HostedCall
	seen := map[string]bool{}
	for _, raw := range items {
		var item struct {
			ID     string          `json:"id"`
			Type   string          `json:"type"`
			Status string          `json:"status"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return nil, errors.New("invalid OpenAI Responses hosted tool item")
		}
		name := responseHostedTools[item.Type]
		if name == "" {
			continue
		}
		if item.ID == "" || len(item.ID) > 256 || seen[item.ID] {
			return nil, errors.New("invalid or repeated OpenAI Responses hosted tool ID")
		}
		seen[item.ID] = true
		failure := item.Status != "" && item.Status != "completed" || len(item.Error) > 0 && string(item.Error) != "null"
		result := string(raw)
		if failure {
			result = `{"error":"Provider-hosted tool failed"}`
		}
		calls = append(calls, HostedCall{ID: item.ID, Name: name, Result: result, Failure: failure})
	}
	return calls, nil
}

func validResponseOutput(items []json.RawMessage, replay bool) bool {
	var hosted []json.RawMessage
	for _, raw := range items {
		if len(raw) == 0 {
			return false
		}
		var item struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return false
		}
		switch item.Type {
		case "reasoning":
			if replay && !validResponseReasoningItem(raw) {
				return false
			}
		case "message":
			var message struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
				} `json:"content"`
			}
			if json.Unmarshal(raw, &message) != nil || message.Role != "assistant" {
				return false
			}
			for _, part := range message.Content {
				if part.Type != "output_text" && part.Type != "refusal" {
					return false
				}
			}
		case "function_call":
			var call responseItem
			if json.Unmarshal(raw, &call) != nil || call.ID == "" || call.CallID == "" || call.Name == "" || len(call.ID) > 256 || len(call.CallID) > 256 || len(call.Name) > 128 || len(call.Arguments) > maxToolArgsBytes {
				return false
			}
			var args map[string]any
			if json.Unmarshal([]byte(call.Arguments), &args) != nil || args == nil {
				return false
			}
		case "file_search_call", "code_interpreter_call", "computer_call", "mcp_call":
			hosted = append(hosted, raw)
		default:
			return false
		}
	}
	_, err := responseHostedCalls(hosted)
	return err == nil
}

func responseReasoningItems(items []responseItem) ([]json.RawMessage, error) {
	var output []json.RawMessage
	for _, item := range items {
		if item.Type != "reasoning" {
			continue
		}
		if item.ID == "" || len(item.ID) > 256 {
			return nil, errors.New("invalid OpenAI Responses reasoning item ID")
		}
		summary := make([]map[string]string, 0, len(item.Summary))
		for _, part := range item.Summary {
			if part.Type != "summary_text" {
				return nil, errors.New("unsupported OpenAI Responses reasoning summary")
			}
			summary = append(summary, map[string]string{"type": "summary_text", "text": part.Text})
		}
		value := map[string]any{"type": "reasoning", "id": item.ID, "summary": summary}
		if item.EncryptedContent != "" {
			value["encrypted_content"] = item.EncryptedContent
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, errors.New("invalid OpenAI Responses reasoning item")
		}
		output = append(output, encoded)
	}
	return output, nil
}

func validResponseReasoningItem(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var item struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Summary []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"summary"`
		EncryptedContent string `json:"encrypted_content"`
	}
	if json.Unmarshal(raw, &item) != nil || item.Type != "reasoning" || item.ID == "" || len(item.ID) > 256 {
		return false
	}
	for _, part := range item.Summary {
		if part.Type != "summary_text" {
			return false
		}
	}
	return true
}
