package generation

import "vista/internal/provider"

// AdvertisedRoute recognizes wire formats declared by a gateway, not the
// vendor-like name of a model or a generic video endpoint shared by protocols.
func AdvertisedRoute(kind string, output []string, api string, endpoints []string) (adapter, outputKind string) {
	outputKind = provider.OutputKind(provider.OutputMetadata{Kind: kind, Output: output})
	kindFor, genericSystemOne := "", false
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
		case "/systemone", "/v1/systemone":
			genericSystemOne = true
			continue
		case "openrouter-decisions", "openrouter-systemone":
			route, produces = "openrouter", "decision"
		case "opencode-systemone":
			route, produces = "opencode", "decision"
		case "openai-decisions":
			route, produces = "openai", "decision"
		case "gemini-decisions":
			route, produces = "google", "decision"
		case "baseten-systemone":
			route, produces = "baseten", "decision"
		case "typesafe-systemone":
			route, produces = "typesafe", "decision"
		case "liquid-ai-systemone":
			route, produces = "liquid-ai", "decision"
		case "subq-systemone":
			route, produces = "subq", "decision"
		case "levanto-sage":
			route, produces = "levanto", "decision"
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
	if adapter == "" && genericSystemOne && (outputKind == "" || outputKind == "decision") {
		// Bare /systemone is the compatible legacy dialect. A qualified
		// protocol above wins instead of being contradicted by this path.
		adapter, kindFor = "openrouter", "decision"
	}
	if outputKind == "" {
		outputKind = kindFor
	}
	return adapter, outputKind
}
