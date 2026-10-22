package opencodeconfig

import (
	"errors"
	"maps"
)

// Providers normalizes the two declarative formats before file layers merge.
// Like OpenCode, a native entry replaces the entire same-name legacy entry in
// one document; later documents can then overlay individual fields.
func Providers(document map[string]any) (map[string]any, error) {
	result := map[string]any{}
	for _, key := range []string{"provider", "providers"} {
		value := document[key]
		if value == nil {
			continue
		}
		entries, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("invalid OpenCode providers")
		}
		for id, value := range entries {
			if key == "provider" {
				value = legacyProvider(value)
				id = ProviderID(id)
			}
			result[id] = value
		}
	}
	return result, nil
}

func ProviderID(id string) string {
	switch id {
	case "azure-cognitive-services":
		return "azure"
	case "google-vertex-anthropic":
		return "google-vertex"
	default:
		return id
	}
}

func legacyProvider(value any) any {
	source, ok := value.(map[string]any)
	if !ok {
		return value
	}
	result := maps.Clone(source)
	delete(result, "npm")
	delete(result, "api")
	delete(result, "options")
	if pack, exists := source["npm"]; exists {
		result["package"] = pack
	}
	if options, exists := source["options"]; exists {
		result["settings"] = options
		if settings, ok := options.(map[string]any); ok {
			settings = maps.Clone(settings)
			for _, key := range []string{"headers", "body"} {
				if value, exists := settings[key]; exists {
					result[key] = value
					delete(settings, key)
				}
			}
			if value, exists := settings["setCacheKey"]; exists {
				result["compatibility"] = map[string]any{"supportsPromptCacheKey": value}
				delete(settings, "setCacheKey")
			}
			result["settings"] = settings
		}
	}
	for _, key := range []string{"whitelist", "blacklist"} {
		if value, exists := source[key]; exists {
			if items, ok := value.([]any); !ok || len(items) > 0 {
				result["compatibility"] = map[string]any{"legacyModelFilter": true}
			}
		}
	}
	if api, exists := source["api"]; exists && api != "" {
		settings, _ := result["settings"].(map[string]any)
		if settings == nil && result["settings"] != nil {
			return result // Preserve invalid options for validation in the importer.
		}
		if settings == nil {
			settings = map[string]any{}
		}
		settings["baseURL"] = api
		result["settings"] = settings
	}
	if models, ok := source["models"].(map[string]any); ok {
		converted := map[string]any{}
		for id, value := range models {
			model, ok := value.(map[string]any)
			if !ok {
				converted[id] = value
				continue
			}
			model = maps.Clone(model)
			if value, exists := model["id"]; exists {
				model["modelID"] = value
			}
			if value, exists := model["options"]; exists {
				model["settings"] = value
			}
			if override, ok := model["provider"].(map[string]any); ok {
				if value, exists := override["npm"]; exists {
					model["package"] = value
				}
				if value, exists := override["api"]; exists {
					settings, _ := model["settings"].(map[string]any)
					settings = maps.Clone(settings)
					if settings == nil {
						settings = map[string]any{}
					}
					settings["baseURL"] = value
					model["settings"] = settings
				}
			}
			converted[id] = model
		}
		result["models"] = converted
	}
	return result
}

func Plugins(document map[string]any) []any {
	legacy, _ := document["plugin"].([]any)
	native, _ := document["plugins"].([]any)
	result := make([]any, 0, len(legacy)+len(native))
	for _, value := range legacy {
		if tuple, ok := value.([]any); ok && len(tuple) > 0 {
			value = tuple[0]
		}
		if name, ok := value.(string); ok {
			value = map[string]any{"package": name}
		}
		result = append(result, value)
	}
	return append(result, native...)
}

func Skills(value any) ([]any, error) {
	if items, ok := value.([]any); ok {
		return items, nil
	}
	legacy, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("invalid OpenCode skill sources")
	}
	items := []any{}
	for _, key := range []string{"paths", "urls"} {
		if value, exists := legacy[key]; exists {
			paths, ok := value.([]any)
			if !ok {
				return nil, errors.New("invalid OpenCode skill sources")
			}
			items = append(items, paths...)
		}
	}
	return items, nil
}
