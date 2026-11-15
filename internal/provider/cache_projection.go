package provider

// Project final wire shapes into common diagnostic sections. Markers may move
// as the tail advances; they do not change the semantic prefix being compared.
// Never recurse into user text, schema properties or tool arguments.
func cacheProjection(body map[string]any) map[string]any {
	result := make(map[string]any, len(body))
	for key, value := range body {
		result[key] = value
	}
	var instructions, items any = body["instructions"], body["input"]
	for _, field := range []string{"messages", "contents", "prompt"} {
		if messages, ok := body[field].([]any); ok {
			var systems, history []any
			leading := true
			for _, item := range messages {
				message, ok := item.(map[string]any)
				if !ok {
					history = append(history, item)
					leading = false
					continue
				}
				clean := cleanCacheBlock(message)
				if role := message["role"]; leading && (role == "system" || role == "developer") {
					systems = append(systems, clean)
				} else {
					history = append(history, clean)
					leading = false
				}
			}
			instructions, items = systems, history
			delete(result, field)
			break
		}
	}
	if system, exists := body["system"]; exists {
		instructions = cleanCacheBlocks(system)
		delete(result, "system")
	}
	if system, exists := body["systemInstruction"]; exists {
		instructions = system
		delete(result, "systemInstruction")
	}
	if tools, ok := body["toolConfig"].(map[string]any); ok {
		result["tools"] = cleanCacheBlocks(tools["tools"])
		delete(result, "toolConfig")
	} else {
		result["tools"] = cleanCacheBlocks(body["tools"])
	}
	result["instructions"], result["input"] = instructions, items
	return result
}

func cleanCacheBlocks(value any) any {
	blocks, ok := value.([]any)
	if !ok {
		return value
	}
	out := make([]any, 0, len(blocks))
	for _, item := range blocks {
		block, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		if block["cachePoint"] != nil {
			continue
		}
		out = append(out, cleanCacheBlock(block))
	}
	return out
}

func cleanCacheBlock(block map[string]any) map[string]any {
	copy := make(map[string]any, len(block))
	for key, value := range block {
		switch key {
		case "cache_control", "cachePoint":
			continue
		case "content":
			value = cleanCacheBlocks(value)
		case "providerOptions":
			options, ok := value.(map[string]any)
			if !ok {
				break
			}
			cloned := make(map[string]any, len(options))
			for provider, settings := range options {
				if provider != "anthropic" && provider != "alibaba" {
					cloned[provider] = settings
					continue
				}
				fields, ok := settings.(map[string]any)
				if !ok {
					cloned[provider] = settings
					continue
				}
				clean := make(map[string]any, len(fields))
				for field, v := range fields {
					if field != "cacheControl" {
						clean[field] = v
					}
				}
				if len(clean) > 0 {
					cloned[provider] = clean
				}
			}
			if len(cloned) == 0 {
				continue
			}
			value = cloned
		}
		copy[key] = value
	}
	return copy
}
