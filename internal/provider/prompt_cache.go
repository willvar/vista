package provider

// Breakpoint selection is adapted from OpenCode's cache-policy.ts and cache.ts
// at 6dd40633a15ec82c5fa2f2c4ea17e22c2ba67084 (MIT, Copyright (c) 2025 opencode).
// See licenses/opencode/LICENSE. Hints are applied only to request-local bodies.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	modelcfg "vista/internal/model"
)

var errCacheBreakpoints = errors.New("prompt caching accepts at most four explicit breakpoints")

type cacheCapabilities struct {
	format                           string
	tools, key, retention, openaiTTL bool
}

func promptCacheCapabilities(model modelcfg.Model) cacheCapabilities {
	c := cacheCapabilities{}
	openai := !model.Custom && slices.Contains([]string{"", "openai", "azure", "azure-cognitive-services"}, model.ProviderID)
	switch model.Provider {
	case "openai-chat", "openai-responses", "bedrock-mantle-chat", "bedrock-mantle-responses":
		if openai || codexRouting(model) {
			c.key = true
		}
		if openai {
			c.format, c.retention = "openai", true
			c.openaiTTL = model.Provider == "openai-responses" && modernOpenAICache(model.ID)
		}
		if !model.Custom && model.ProviderID == "openrouter" {
			c.format, c.tools = gatewayCacheFormat(model.ID)
		}
		if !model.Custom && alibabaProvider(model.ProviderID) && model.Provider == "openai-chat" && strings.HasPrefix(strings.ToLower(model.ID), "qwen") {
			c.format = "alibaba"
		}
	case "anthropic", "vertex-anthropic":
		if !model.Custom {
			c.format, c.tools = "anthropic", true
		}
	case "bedrock":
		if supportedBedrockCache(model.ID) {
			c.format, c.tools = "bedrock", true
		}
	case "gemini", "vertex-gemini":
		c.format = "gemini"
	case "vercel-gateway":
		c.format, c.tools = gatewayCacheFormat(model.ID)
	}
	if options := model.PromptCaching; options != nil {
		switch options.Format {
		case "", "auto":
		case "none":
			c = cacheCapabilities{}
		case "openai":
			c.format, c.key, c.retention = "openai", true, true
			// An explicit format opts opaque compatible model IDs into native options.
			c.openaiTTL = model.Provider == "openai-responses" && (model.Custom || modernOpenAICache(model.ID))
		case "anthropic", "bedrock", "gemini":
			c.format, c.tools = options.Format, true
		case "alibaba":
			c.format, c.tools = "alibaba", false
		}
		if options.Key != nil {
			c.key = *options.Key
		}
	}
	return c
}

func gatewayCacheFormat(id string) (string, bool) {
	id = strings.TrimPrefix(id, "~")
	if strings.HasPrefix(id, "anthropic/") {
		return "anthropic", true
	}
	if strings.HasPrefix(id, "qwen/") || strings.HasPrefix(id, "alibaba/qwen") {
		return "alibaba", false // Qwen does not accept tool-definition markers.
	}
	return "", false
}

func modernOpenAICache(id string) bool {
	var generation, minor int
	if _, err := fmt.Sscanf(id, "gpt-%d.%d", &generation, &minor); err == nil {
		return generation > 5 || generation == 5 && minor >= 6
	}
	return false
}

func stableCacheKey(key string) string {
	if len(key) <= 64 {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func promptCacheKey(model modelcfg.Model) string {
	if !promptCacheCapabilities(model).key || model.SessionID == "" {
		return ""
	}
	key := model.PromptCacheKey
	if key == "" {
		key = model.SessionID
	}
	return stableCacheKey(key)
}

// ValidatePromptCaching also runs at the connection boundary and before direct
// adapter requests, so malformed or contradictory options cannot be ignored.
func ValidatePromptCaching(model modelcfg.Model) error {
	o := model.PromptCaching
	if o == nil {
		return nil
	}
	if !slices.Contains([]string{"", "auto", "none", "openai", "anthropic", "alibaba", "bedrock", "gemini"}, o.Format) {
		return errors.New("invalid promptCaching format")
	}
	chat := model.Provider == "openai-chat" || model.Provider == "openai-responses" || strings.HasPrefix(model.Provider, "bedrock-mantle-")
	anthropic := model.Provider == "anthropic" || model.Provider == "vertex-anthropic" || model.Provider == "openai-chat" || model.Provider == "vercel-gateway"
	gemini := model.Provider == "gemini" || model.Provider == "vertex-gemini"
	if o.Format == "openai" && !chat || (o.Format == "anthropic" || o.Format == "alibaba") && !anthropic || o.Format == "bedrock" && model.Provider != "bedrock" || o.Format == "gemini" && !gemini {
		return errors.New("promptCaching format does not match the API format")
	}
	c := promptCacheCapabilities(model)
	if o.Key != nil && (!chat || *o.Key && o.Format == "none") {
		return errors.New("promptCaching key requires a compatible Chat or Responses API")
	}
	if o.Retention != "" && (!c.retention || !slices.Contains([]string{"in_memory", "24h"}, o.Retention) || o.TTL != "") {
		return errors.New("promptCaching retention requires OpenAI compatibility and cannot be combined with ttl")
	}
	if o.TTL != "" {
		valid := (c.format == "anthropic" || c.format == "bedrock") && slices.Contains([]string{"5m", "1h"}, o.TTL) || c.openaiTTL && slices.Contains([]string{"30m", "12h"}, o.TTL)
		if !valid {
			return errors.New("promptCaching ttl is unsupported for this API and model")
		}
	}
	if ref := o.CachedContent; ref != nil {
		digest, err := hex.DecodeString(ref.SHA256)
		if !gemini || c.format != "gemini" || len(ref.Name) > 1024 || !(strings.HasPrefix(ref.Name, "cachedContents/") || strings.HasPrefix(ref.Name, "projects/") && strings.Contains(ref.Name, "/cachedContents/")) || strings.ContainsAny(ref.Name, "?#\r\n\t ") || ref.Messages < 0 || err != nil || len(digest) != sha256.Size {
			return errors.New("invalid Gemini cachedContent prefix binding")
		}
	}
	return nil
}

// JSON cloning keeps schemas and replay blocks untouched and preserves large
// integer tool arguments. Only request compilation uses the resulting maps.
func cacheJSONBody(body map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var cloned map[string]any
	err = decoder.Decode(&cloned)
	return cloned, err
}

func applyPromptCaching(model modelcfg.Model, body map[string]any, messages []ChatMessage) (map[string]any, error) {
	if err := ValidatePromptCaching(model); err != nil {
		return nil, err
	}
	c := promptCacheCapabilities(model)
	if c.key && model.ProviderID != "xai" {
		if key := promptCacheKey(model); key != "" {
			body["prompt_cache_key"] = key
		}
	}
	if o := model.PromptCaching; o != nil {
		if o.Retention != "" {
			body["prompt_cache_retention"] = o.Retention
		}
		if c.openaiTTL && o.TTL != "" {
			body["prompt_cache_options"] = map[string]any{"ttl": o.TTL}
		}
		if o.CachedContent != nil {
			return applyGeminiCachedContent(body, o.CachedContent)
		}
	}
	if c.format != "anthropic" && c.format != "alibaba" {
		return body, nil
	}
	cloned, err := cacheJSONBody(body)
	if err != nil {
		return nil, err
	}
	if model.Provider == "anthropic" || model.Provider == "vertex-anthropic" {
		var system []any
		for _, message := range messages {
			if message.Role == "system" && message.Content != "" {
				system = append(system, map[string]any{"type": "text", "text": message.Content})
			}
		}
		if len(system) > 0 {
			cloned["system"] = system
		}
	}
	if err := applyCacheBreakpoints(cloned, model, c); err != nil {
		return nil, err
	}
	return cloned, nil
}

// Marker targets are actual content/tool blocks, never nested tool arguments.
// Manual markers consume the shared budget. Automatic targets are the last tool,
// first/last distinct system block, and last eligible conversation block.
func applyCacheBreakpoints(body map[string]any, model modelcfg.Model, capabilities cacheCapabilities) error {
	var all, tools, system, tail []map[string]any
	namespace := capabilities.format
	marker := func(block map[string]any) bool {
		if model.Provider == "vercel-gateway" {
			options, _ := block["providerOptions"].(map[string]any)
			provider, _ := options[namespace].(map[string]any)
			return provider["cacheControl"] != nil
		}
		return block["cache_control"] != nil
	}
	add := func(block map[string]any, group *[]map[string]any) {
		if block == nil {
			return
		}
		all = append(all, block)
		if kind, _ := block["type"].(string); kind == "thinking" || kind == "redacted_thinking" || kind == "reasoning" {
			return
		}
		*group = append(*group, block)
	}
	if items, ok := body["tools"].([]any); ok {
		for _, item := range items {
			block, _ := item.(map[string]any)
			add(block, &tools)
		}
	}
	if items, ok := body["system"].([]any); ok {
		for _, item := range items {
			block, _ := item.(map[string]any)
			add(block, &system)
		}
	}
	field := "messages"
	if model.Provider == "vercel-gateway" {
		field = "prompt"
	}
	items, _ := body[field].([]any)
	for _, item := range items {
		message, _ := item.(map[string]any)
		role, _ := message["role"].(string)
		var messageTail []map[string]any
		group := &messageTail
		if role == "system" || role == "developer" {
			group = &system
		}
		if text, ok := message["content"].(string); ok && text != "" {
			if model.Provider == "vercel-gateway" && role == "system" {
				add(message, group) // AI SDK system messages carry options on the message.
				continue
			}
			message["content"] = []any{map[string]any{"type": "text", "text": text}}
		}
		if blocks, ok := message["content"].([]any); ok {
			for _, block := range blocks {
				m, _ := block.(map[string]any)
				add(m, group)
			}
		}
		if len(messageTail) > 0 {
			tail = messageTail
		}
	}
	remaining := 4
	for _, block := range all {
		if marker(block) {
			remaining--
		}
	}
	if remaining < 0 {
		return errCacheBreakpoints
	}
	// Anthropic requires longer-lived checkpoints before shorter-lived ones.
	// Preserve manual hints and skip automatic placements that violate that order.
	validLifetimeOrder := func() bool {
		shortSeen := false
		for _, block := range all {
			var value any = block["cache_control"]
			if model.Provider == "vercel-gateway" {
				options, _ := block["providerOptions"].(map[string]any)
				provider, _ := options[namespace].(map[string]any)
				value = provider["cacheControl"]
			}
			control, ok := value.(map[string]any)
			if !ok {
				continue
			}
			if control["ttl"] == "1h" {
				if shortSeen {
					return false
				}
			} else {
				shortSeen = true
			}
		}
		return true
	}
	if !validLifetimeOrder() {
		return errors.New("prompt-cache checkpoints must place 1h lifetimes before 5m lifetimes")
	}
	mark := func(block map[string]any) {
		if remaining <= 0 || marker(block) {
			return
		}
		control := map[string]any{"type": "ephemeral"}
		if model.PromptCaching != nil && model.PromptCaching.TTL != "" {
			control["ttl"] = model.PromptCaching.TTL
		}
		if model.Provider == "vercel-gateway" {
			options, _ := block["providerOptions"].(map[string]any)
			if options == nil {
				options = map[string]any{}
				block["providerOptions"] = options
			}
			provider, _ := options[namespace].(map[string]any)
			if provider == nil {
				provider = map[string]any{}
				options[namespace] = provider
			}
			provider["cacheControl"] = control
		} else {
			block["cache_control"] = control
		}
		if !validLifetimeOrder() {
			if model.Provider == "vercel-gateway" {
				options := block["providerOptions"].(map[string]any)
				provider := options[namespace].(map[string]any)
				delete(provider, "cacheControl")
				if len(provider) == 0 {
					delete(options, namespace)
				}
				if len(options) == 0 {
					delete(block, "providerOptions")
				}
			} else {
				delete(block, "cache_control")
			}
			return
		}
		remaining--
	}
	if capabilities.tools && len(tools) > 0 {
		mark(tools[len(tools)-1])
	}
	if len(system) > 0 {
		mark(system[0])
		if len(system) > 1 {
			mark(system[len(system)-1])
		}
	}
	if len(tail) > 0 {
		target := tail[len(tail)-1]
		for _, block := range tail {
			if block["type"] == "text" {
				target = block
			}
		}
		mark(target)
	}
	return nil
}

func applyGeminiCachedContent(body map[string]any, ref *modelcfg.CachedContent) (map[string]any, error) {
	cloned, err := cacheJSONBody(body)
	if err != nil {
		return nil, err
	}
	contents, _ := cloned["contents"].([]any)
	if ref.Messages >= len(contents) {
		return nil, errors.New("Gemini cachedContent must leave new contents to generate from")
	}
	prefix := map[string]any{"systemInstruction": cloned["systemInstruction"], "tools": cloned["tools"], "contents": contents[:ref.Messages]}
	encoded, err := json.Marshal(prefix)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(encoded)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), ref.SHA256) {
		return nil, errors.New("Gemini cachedContent prefix changed; update the resource binding or remove it")
	}
	delete(cloned, "systemInstruction")
	delete(cloned, "tools")
	cloned["contents"], cloned["cachedContent"] = contents[ref.Messages:], ref.Name
	return cloned, nil
}
