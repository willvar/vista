package generation

import "vista/internal/provider"

// AdvertisedRoute recognizes wire formats declared by a gateway, not the
// vendor-like name of a model or a generic video endpoint shared by protocols.
func AdvertisedRoute(kind string, output []string, api string, endpoints []string) (adapter, outputKind string) {
	outputKind = provider.OutputKind(provider.OutputMetadata{Kind: kind, Output: output})
	kindFor := ""
	for _, hint := range append([]string{api}, endpoints...) {
		var route, produces string
		switch hint {
		case "openai-images", "/images/generations", "/v1/images/generations":
			route, produces = "openai", "image"
		case "openrouter-images":
			route, produces = "openrouter", "image"
		case "openrouter-video":
			route, produces = "openrouter", "video"
		case "xai-video":
			route, produces = "xai", "video"
		case "deepinfra-video":
			route, produces = "deepinfra", "video"
		case "google-video":
			route, produces = "google", "video"
		case "google-images":
			route, produces = "google", "image"
		case "openrouter-decisions", "/systemone":
			route, produces = "openrouter", "decision"
		default:
			continue
		}
		if outputKind != "" && outputKind != produces {
			continue
		}
		if adapter != "" && (adapter != route || kindFor != produces) {
			return "", outputKind
		}
		adapter, kindFor = route, produces
	}
	if outputKind == "" {
		outputKind = kindFor
	}
	return adapter, outputKind
}
