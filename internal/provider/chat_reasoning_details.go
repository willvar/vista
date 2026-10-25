package provider

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Chat-compatible gateways can send visible or opaque reasoning details.
// Keep the supported wire objects for tool continuation, while limiting the
// amount that one turn can retain and replay.
const maxChatReasoningDetailsBytes = 64 << 10
const maxChatReasoningDetailBlocks = 256

type chatReasoningDetails struct {
	items     []map[string]json.RawMessage
	sizes     []int
	bytes     int
	fragments int
}

func chatMessagesWithReasoningDetails(messages []ChatMessage, providerID string) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		item := map[string]any{"role": message.Role, "content": message.Content}
		images, err := imagePayloads(message)
		if err != nil {
			return nil, err
		}
		if len(images) > 0 {
			parts := []any{}
			if message.Content != "" {
				parts = append(parts, map[string]any{"type": "text", "text": message.Content})
			}
			for _, url := range images {
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
			}
			item["content"] = parts
		}
		if message.Role == "assistant" && len(message.ToolCalls) > 0 && message.Content == "" {
			item["content"] = nil
		}
		if message.ToolCallID != "" {
			item["tool_call_id"] = message.ToolCallID
		}
		if len(message.ToolCalls) > 0 {
			item["tool_calls"] = message.ToolCalls
		}
		if len(message.ReasoningDetails) > 0 {
			if message.Role != "assistant" {
				return nil, errors.New("reasoning_details require an assistant message")
			}
			var details chatReasoningDetails
			for _, raw := range message.ReasoningDetails {
				wrapped := append(append([]byte{'['}, raw...), ']')
				if _, err := details.add(wrapped); err != nil {
					return nil, err
				}
			}
			item["reasoning_details"] = message.ReasoningDetails
		}
		if message.ReasoningText != "" {
			if message.Role != "assistant" {
				return nil, errors.New("reasoning replay text requires an assistant message")
			}
			if len(message.ReasoningText) > maxChatReasoningDetailsBytes {
				return nil, errors.New("model reasoning replay text exceeds 64 KiB")
			}
			field, err := chatReasoningReplayField(providerID, message)
			if err != nil {
				return nil, err
			}
			if field != "" {
				item[field] = message.ReasoningText
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func chatReasoningReplayField(providerID string, message ChatMessage) (string, error) {
	switch message.ReasoningField {
	case "", "reasoning_content", "reasoning", "reasoning_text":
	default:
		return "", errors.New("unsupported model reasoning replay field")
	}
	if moonshotProvider(providerID) || alibabaProvider(providerID) {
		return "reasoning_content", nil
	}
	if message.ReasoningField != "" {
		return message.ReasoningField, nil
	}
	if hasKimiReasoningDetail(message.ReasoningDetails) {
		return "reasoning_content", nil
	}
	if providerID == "openrouter" {
		return "reasoning", nil
	}
	if len(message.ReasoningDetails) == 0 {
		return "reasoning_content", nil
	}
	return "", nil
}

func alibabaProvider(id string) bool {
	switch id {
	case "alibaba", "alibaba-cn", "alibaba-coding-plan", "alibaba-coding-plan-cn", "alibaba-token-plan", "alibaba-token-plan-cn":
		return true
	}
	return false
}

func moonshotProvider(id string) bool {
	return id == "moonshotai" || id == "moonshotai-cn" || id == "kimi-for-coding"
}

func hasKimiReasoningDetail(items []json.RawMessage) bool {
	for _, raw := range items {
		var item struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &item) == nil && (item.Type == "summary" || item.Type == "encrypted") {
			return true
		}
	}
	return false
}

func hasStructuredVisibleDetail(raw json.RawMessage) bool {
	var items []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return false
	}
	for _, item := range items {
		if item.Type == "reasoning.text" || item.Type == "reasoning.summary" {
			return true
		}
	}
	return false
}

func (d *chatReasoningDetails) add(raw json.RawMessage) (string, error) {
	var incoming []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &incoming); err != nil || incoming == nil {
		return "", errors.New("invalid model reasoning_details")
	}
	var visible string
	for _, item := range incoming {
		d.fragments++
		if item == nil {
			return "", errors.New("invalid model reasoning detail")
		}
		var kind string
		if json.Unmarshal(item["type"], &kind) != nil {
			return "", errors.New("model reasoning detail lacks a type")
		}
		var field string
		switch kind {
		case "reasoning.text":
			field = "text"
		case "reasoning.summary", "summary":
			field = "summary"
		case "reasoning.encrypted":
			field = "data"
		case "encrypted":
			field = "encrypted"
		default:
			return "", fmt.Errorf("unsupported model reasoning detail %q", kind)
		}
		var content string
		if value, present := item[field]; present {
			if json.Unmarshal(value, &content) != nil {
				return "", errors.New("invalid model reasoning detail content")
			}
		} else if kind == "reasoning.encrypted" || kind == "encrypted" {
			return "", errors.New("invalid model reasoning detail content")
		}
		if kind == "reasoning.text" || kind == "reasoning.summary" || kind == "summary" {
			if len(content) > maxReplyBytes-len(visible) {
				return "", errors.New("model reasoning exceeds 256 KiB")
			}
			visible += content
		}
		// A streamed delta is not an independent reasoning block. Merge before
		// applying retained-history limits, without mutating accepted state on error.
		target := d.mergeTarget(item)
		candidate := item
		oldBytes := 0
		if target >= 0 {
			candidate = make(map[string]json.RawMessage, len(d.items[target]))
			for key, value := range d.items[target] {
				candidate[key] = value
			}
			mergeChatReasoningDetail(candidate, item)
			oldBytes = d.sizes[target]
		} else if len(d.items) >= maxChatReasoningDetailBlocks {
			return "", errors.New("model reasoning_details exceed 256 independent blocks")
		}
		encoded, err := json.Marshal(candidate)
		if err != nil {
			return "", errors.New("invalid model reasoning detail")
		}
		if d.bytes-oldBytes+len(encoded) > maxChatReasoningDetailsBytes {
			return "", errors.New("model reasoning_details exceed 64 KiB")
		}
		d.bytes += len(encoded) - oldBytes
		if target >= 0 {
			d.items[target], d.sizes[target] = candidate, len(encoded)
		} else {
			d.items = append(d.items, candidate)
			d.sizes = append(d.sizes, len(encoded))
		}
	}
	return visible, nil
}

func (d *chatReasoningDetails) replay() ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(d.items))
	for _, item := range d.items {
		copy := make(map[string]json.RawMessage, len(item))
		for key, value := range item {
			if key == "index" {
				var kind string
				_ = json.Unmarshal(item["type"], &kind)
				if kind == "summary" || kind == "encrypted" {
					continue // Kimi's index is a streaming position, not replay content.
				}
			}
			copy[key] = value
		}
		encoded, err := json.Marshal(copy)
		if err != nil {
			return nil, err
		}
		out = append(out, encoded)
	}
	return out, nil
}

func (d *chatReasoningDetails) mergeTarget(current map[string]json.RawMessage) int {
	for i := len(d.items) - 1; i >= 0; i-- {
		previous := d.items[i]
		// Nonadjacent fragments require an explicit shared identity. Anonymous
		// blocks can only continue their immediate predecessor.
		if i != len(d.items)-1 {
			identified := false
			for _, key := range []string{"id", "index"} {
				value := string(current[key])
				if value != "" && value != "null" && value != `""` && value == string(previous[key]) {
					identified = true
				}
			}
			if !identified {
				continue
			}
		}
		if compatibleChatReasoningDetail(previous, current) {
			return i
		}
	}
	return -1
}

func mergeChatReasoningDetail(previous, current map[string]json.RawMessage) bool {
	if !compatibleChatReasoningDetail(previous, current) {
		return false
	}
	var kind string
	_ = json.Unmarshal(current["type"], &kind)
	field := "text"
	if kind != "reasoning.text" {
		field = "summary"
	}
	var old, next string
	_ = json.Unmarshal(previous[field], &old)
	_ = json.Unmarshal(current[field], &next)
	combined, _ := json.Marshal(old + next)
	for key, value := range current {
		if key != field && len(value) > 0 && string(value) != "null" && string(value) != `""` {
			previous[key] = value
		}
	}
	previous[field] = combined
	return true
}

func compatibleChatReasoningDetail(previous, current map[string]json.RawMessage) bool {
	var kind string
	_ = json.Unmarshal(current["type"], &kind)
	if kind != "reasoning.text" && kind != "reasoning.summary" && kind != "summary" || string(previous["type"]) != string(current["type"]) {
		return false
	}
	for _, field := range []string{"id", "index", "format", "signature"} {
		old, next := string(previous[field]), string(current[field])
		if old != "" && old != "null" && next != "" && next != "null" && old != next {
			return false
		}
	}
	return true
}
