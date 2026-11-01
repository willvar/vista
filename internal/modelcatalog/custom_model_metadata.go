package modelcatalog

import (
	"encoding/json"
	"slices"
	"strings"

	modelcfg "vista/internal/model"
)

// Inventory metadata is optional and not standardized. Decode supported fields
// independently: one malformed extension must not discard a model or a page.
type discoveryFields map[string]json.RawMessage

func discoveryObject(raw json.RawMessage) discoveryFields {
	var fields discoveryFields
	_ = json.Unmarshal(raw, &fields)
	return fields
}

func (f discoveryFields) object(key string) discoveryFields { return discoveryObject(f[key]) }

func (f discoveryFields) text(keys ...string) string {
	for _, key := range keys {
		var value string
		if json.Unmarshal(f[key], &value) == nil && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (f discoveryFields) boolean(keys ...string) *bool {
	for _, key := range keys {
		var value *bool
		if json.Unmarshal(f[key], &value) == nil && value != nil {
			return value
		}
	}
	return nil
}

func (f discoveryFields) positive(keys ...string) int {
	for _, key := range keys {
		var value int64
		// Keep arithmetic and worker transport safe on all supported platforms.
		if json.Unmarshal(f[key], &value) == nil && value > 0 && value <= 1<<30 {
			return int(value)
		}
	}
	return 0
}

func (f discoveryFields) strings(keys ...string) []string {
	for _, key := range keys {
		var values []string
		if json.Unmarshal(f[key], &values) == nil && values != nil {
			return values
		}
	}
	return nil
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func firstBoolean(values ...*bool) *bool {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func inventoryModalities(fields ...[]string) []string {
	for _, values := range fields {
		var modalities []string
		for _, value := range values {
			value = strings.ToLower(strings.TrimSpace(value))
			if slices.Contains([]string{"text", "image", "audio", "video", "file"}, value) && !slices.Contains(modalities, value) {
				modalities = append(modalities, value)
			}
		}
		if len(modalities) > 0 {
			return modalities
		}
	}
	return nil
}

func (m *Configured) applyDiscoveredMetadata(row discoveryFields) {
	metadata, capabilities := row.object("metadata"), row.object("capabilities")
	limits, top := row.object("limits"), row.object("top_provider")
	architecture, modalities := row.object("architecture"), row.object("modalities")
	name := row.text("display_name", "displayName", "name")
	if name != m.ID && name != "models/"+m.ID && len(name) <= 300 {
		m.discoveredName = name
	}
	context := firstPositive(row.positive("max_model_len", "context_length", "contextWindow"), top.positive("context_length"))
	input := firstPositive(limits.positive("max_input_tokens", "input"), row.positive("inputTokenLimit"))
	output := firstPositive(limits.positive("max_output_tokens", "output"), row.positive("max_output_tokens", "max_completion_tokens"), top.positive("max_completion_tokens"), row.positive("max_tokens", "outputTokenLimit"))
	if context == 0 && input > 0 && output > 0 && input+output <= 1<<30 {
		context = input + output
	}
	if context > 0 && (m.Context == 0 || context < m.Context) {
		m.Context = context
	}
	m.inputLimit, m.outputLimit = input, output
	tools := firstBoolean(row.boolean("supports_tools", "supports_tool_calls"), capabilities.boolean("tools", "tool_calls"), metadata.boolean("supports_tools", "supports_tool_calls"))
	if parameters := row.strings("supported_parameters"); parameters != nil && tools == nil {
		value := slices.Contains(parameters, "tools") || slices.Contains(parameters, "tool_choice")
		tools = &value
	}
	if tools != nil && (m.Tools == nil || !*tools) {
		m.Tools = tools
	}
	m.inputModalities = inventoryModalities(row.strings("input_modalities", "inputModalities"), architecture.strings("input_modalities"), modalities.strings("input"))
	m.outputModalities = inventoryModalities(row.strings("output_modalities", "outputModalities"), architecture.strings("output_modalities"), modalities.strings("output"))
	if vision := firstBoolean(row.boolean("supports_vision"), capabilities.boolean("vision")); vision != nil && m.inputModalities == nil {
		m.inputModalities = []string{"text"}
		if *vision {
			m.inputModalities = append(m.inputModalities, "image")
		}
	}
	// An explicit user input restriction can narrow discovered capabilities.
	if len(m.InputModalities) > 0 && len(m.inputModalities) > 0 {
		m.inputModalities = slices.DeleteFunc(m.inputModalities, func(value string) bool { return !slices.Contains(m.InputModalities, value) })
	}
	m.discoveredReasoning = inventoryReasoning(row, metadata, capabilities)
	m.metadataKnown = m.metadataKnown || context > 0 || input > 0 || output > 0 || tools != nil || len(m.inputModalities) > 0 || len(m.outputModalities) > 0 || m.discoveredReasoning != nil
}

func inventoryReasoning(fields ...discoveryFields) *modelcfg.ReasoningCapabilities {
	var supported *bool
	var options []modelcfg.ReasoningOption
	for _, fields := range fields {
		if supported == nil {
			supported = firstBoolean(fields.boolean("reasoning"), fields.object("reasoning").boolean("supported"), fields.boolean("thinking"))
		}
		if options != nil {
			continue
		}
		levels := fields.strings("supported_reasoning_levels")
		if levels == nil {
			levels = fields.object("reasoning").strings("effort")
		}
		if levels != nil {
			var valid []string
			for _, value := range levels {
				value = strings.ToLower(strings.TrimSpace(value))
				if slices.Contains([]string{"default", "none", "minimal", "low", "medium", "high", "xhigh", "max"}, value) && !slices.Contains(valid, value) {
					valid = append(valid, value)
				}
			}
			if len(valid) > 0 {
				options = []modelcfg.ReasoningOption{{Type: "effort", Values: valid}}
			} else if len(levels) == 0 {
				options = []modelcfg.ReasoningOption{}
			}
		}
	}
	if supported == nil && options == nil {
		return nil
	}
	return &modelcfg.ReasoningCapabilities{Supported: supported == nil || *supported, Options: options}
}

// WithDiscoveryMetadata preserves connection identity and explicit restrictions
// while applying the metadata for this exact inventory model.
func (m Configured) WithDiscoveryMetadata(remote Configured) Configured {
	if remote.Context > 0 && (m.Context == 0 || remote.Context < m.Context) {
		m.Context = remote.Context
	}
	if remote.Tools != nil && (m.Tools == nil || !*remote.Tools) {
		m.Tools = remote.Tools
	}
	m.inputLimit, m.outputLimit = remote.inputLimit, remote.outputLimit
	m.inputModalities, m.outputModalities = slices.Clone(remote.inputModalities), slices.Clone(remote.outputModalities)
	if len(m.InputModalities) > 0 && m.inputModalities != nil {
		m.inputModalities = slices.DeleteFunc(m.inputModalities, func(value string) bool { return !slices.Contains(m.InputModalities, value) })
	}
	m.discoveredName, m.discoveredReasoning = remote.discoveredName, remote.discoveredReasoning
	m.metadataKnown, m.discoverySource = remote.metadataKnown, remote.discoverySource
	return m
}
