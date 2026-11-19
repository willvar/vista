package generation

import (
	"slices"

	"vista/internal/model"
	"vista/internal/provider"
)

// These are adapter-specific candidate IDs from the pinned upstream contracts,
// not a display-name source or a guarantee of account entitlement. Services
// with per-account inventories or deployment-specific IDs supply those separately.
var declaredChoices = map[string]map[string][]string{
	model.ImageOutput: {
		"openai":         {"gpt-image-2", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst", "gpt-image-1.5", "gpt-image-1-mini"},
		"openai-codex":   {"gpt-image-2"},
		"openrouter":     {"google/gemini-3.1-flash-image-preview"},
		"google":         {"gemini-3.1-flash-image"},
		"xai":            {"grok-imagine-image"},
		"deepinfra":      {"black-forest-labs/FLUX-1-schnell"},
		"fal":            {"fal-ai/flux/dev"},
		"litellm":        {"gpt-image-2"},
		"comfy":          {"workflow"},
		"minimax":        {"image-01"},
		"minimax-portal": {"image-01"},
		"vydra":          {"grok-imagine"},
	},
	model.VideoOutput: {
		"alibaba":        {"wan2.6-t2v", "wan2.6-i2v"},
		"byteplus":       {"seedance-1-0-pro-250528", "seedance-1-5-pro-251215", "dreamina-seedance-2-0-260128"},
		"comfy":          {"workflow"},
		"deepinfra":      {"Pixverse/Pixverse-T2V"},
		"fal":            {"fal-ai/minimax/video-01-live"},
		"google":         {"veo-3.1-fast-generate-preview"},
		"kie":            {"kling-2.6/text-to-video"},
		"minimax":        {"MiniMax-Hailuo-2.3"},
		"minimax-portal": {"MiniMax-Hailuo-2.3"},
		"novita":         {"wan2.6-t2v", "wan2.6-i2v"},
		"openrouter":     {"google/veo-3.1-fast"},
		"pixverse":       {"v6"},
		"qwen":           {"wan2.6-t2v", "wan2.6-i2v"},
		"runway":         {"gen4.5"},
		"together":       {"Wan-AI/wan2.7-t2v", "Wan-AI/wan2.7-i2v"},
		"vydra":          {"veo3"},
		"xai":            {"grok-imagine-video"},
		"zai":            {"cogvideox-3"},
	},
	model.DecisionOutput: {
		"baseten":    {"decision-models/baseten-elephant-9b-latest", "decision-models/baseten-mammoth-27b-latest"},
		"clawrouter": {"openai/gpt-6-luna"},
		"google":     {"gemini-decision-1"},
		"levanto":    {"levanto-sage"},
		"liquid-ai":  {"d1", "d1-ultra"},
		"openai":     {"gpt-6-luna"},
		"opencode":   {"jev-1.13", "jev-1.13-free", "gpt-6-luna-decision", "gemini-decision-1"},
		"openrouter": {"typesafe/jev-1.13", "typesafe/jev-1.14", "~typesafe/jev-latest"},
		"subq":       {"subq-snap-preview"},
		"typesafe":   {"jev-latest", "jev-preview", "jev-1.13.0"},
	},
}

func DeclaredChoice(adapter, kind, id string) bool {
	return slices.Contains(declaredChoices[kind][adapter], id)
}

func Choices(adapter, kind string) []provider.OutputMetadata {
	return provider.SpecializedModels(CatalogProviderID(adapter), kind, declaredChoices[kind][adapter]...)
}

// Pin the model-specific first-frame rules that OpenClaw normally implements
// by switching IDs. Vista instead keeps the user's selected model unchanged.
func VideoFrameMode(adapter, id string) (allowed, required bool) {
	spec, _ := Find(adapter, model.VideoOutput)
	switch adapter {
	case "alibaba", "qwen", "novita":
		switch id {
		case "wan2.6-t2v":
			return false, false
		case "wan2.6-i2v":
			return true, true
		}
	case "together":
		switch id {
		case "Wan-AI/wan2.7-t2v":
			return false, false
		case "Wan-AI/wan2.7-i2v":
			return true, true
		}
	}
	return spec.Frame, false
}
