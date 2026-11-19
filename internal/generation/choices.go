package generation

import (
	"slices"

	"vista/internal/model"
	"vista/internal/provider"
)

type namedModel struct{ id, name string }

// This is the set of usable starting choices published by the pinned OpenClaw
// adapters, not a guess from model IDs or a guarantee of account entitlement.
// A service with deployment-specific IDs or a dynamic catalog advertises them
// separately. OpenCode uses the separately documented Console System One route.
var declaredChoices = map[string]map[string][]namedModel{
	model.ImageOutput: {
		"openai":         {{"gpt-image-2", "GPT-Image-2"}, {"gpt-image-2.5-flare", "GPT Image 2.5 Flare"}, {"gpt-image-2.5-sunburst", "GPT Image 2.5 Sunburst"}, {"gpt-image-1.5", "GPT Image 1.5"}, {"gpt-image-1-mini", "GPT Image 1 Mini"}},
		"openai-codex":   {{"gpt-image-2", "GPT-Image-2"}},
		"openrouter":     {{"google/gemini-3.1-flash-image-preview", "Gemini 3.1 Flash Image"}},
		"google":         {{"gemini-3.1-flash-image", "Gemini 3.1 Flash Image"}},
		"xai":            {{"grok-imagine-image", "Grok Imagine Image"}},
		"deepinfra":      {{"black-forest-labs/FLUX-1-schnell", "FLUX.1 Schnell"}},
		"fal":            {{"fal-ai/flux/dev", "FLUX.1 Dev"}},
		"litellm":        {{"gpt-image-2", "GPT-Image-2"}},
		"comfy":          {{"workflow", "ComfyUI"}},
		"minimax":        {{"image-01", "MiniMax Image 01"}},
		"minimax-portal": {{"image-01", "MiniMax Image 01"}},
		"vydra":          {{"grok-imagine", "Grok Imagine"}},
	},
	model.VideoOutput: {
		"alibaba":        {{"wan2.6-t2v", "Wan 2.6 Text to Video"}, {"wan2.6-i2v", "Wan 2.6 Image to Video"}},
		"byteplus":       {{"seedance-1-0-pro-250528", "Seedance 1.0 Pro"}, {"seedance-1-5-pro-251215", "Seedance 1.5 Pro"}, {"dreamina-seedance-2-0-260128", "Seedance 2.0"}},
		"comfy":          {{"workflow", "ComfyUI"}},
		"deepinfra":      {{"Pixverse/Pixverse-T2V", "PixVerse Text to Video"}},
		"fal":            {{"fal-ai/minimax/video-01-live", "MiniMax Video 01 Live"}},
		"google":         {{"veo-3.1-fast-generate-preview", "Veo 3.1 Fast"}},
		"kie":            {{"kling-2.6/text-to-video", "Kling 2.6 Text to Video"}},
		"minimax":        {{"MiniMax-Hailuo-2.3", "MiniMax Hailuo 2.3"}},
		"minimax-portal": {{"MiniMax-Hailuo-2.3", "MiniMax Hailuo 2.3"}},
		"novita":         {{"wan2.6-t2v", "Wan 2.6 Text to Video"}, {"wan2.6-i2v", "Wan 2.6 Image to Video"}},
		"openrouter":     {{"google/veo-3.1-fast", "Veo 3.1 Fast"}},
		"pixverse":       {{"v6", "PixVerse v6"}},
		"qwen":           {{"wan2.6-t2v", "Wan 2.6 Text to Video"}, {"wan2.6-i2v", "Wan 2.6 Image to Video"}},
		"runway":         {{"gen4.5", "Runway Gen-4.5"}},
		"together":       {{"Wan-AI/wan2.7-t2v", "Wan 2.7 Text to Video"}, {"Wan-AI/wan2.7-i2v", "Wan 2.7 Image to Video"}},
		"vydra":          {{"veo3", "Veo 3"}},
		"xai":            {{"grok-imagine-video", "Grok Imagine Video"}},
		"zai":            {{"cogvideox-3", "CogVideoX-3"}},
	},
	model.DecisionOutput: {
		"baseten":    {{"decision-models/baseten-elephant-9b-latest", "Elephant 1 (9B)"}, {"decision-models/baseten-mammoth-27b-latest", "Mammoth 1 (27B)"}},
		"clawrouter": {{"openai/gpt-6-luna", "GPT-6 Luna"}},
		"google":     {{"gemini-decision-1", "Gemini Decision 1"}},
		"levanto":    {{"levanto-sage", "Sage"}},
		"liquid-ai":  {{"d1", "Liquid AI d1"}, {"d1-ultra", "Liquid AI d1 Ultra"}},
		"openai":     {{"gpt-6-luna", "GPT-6 Luna"}},
		"opencode":   {{"jev-1.13", "Jev 1.13"}, {"jev-1.13-free", "Jev 1.13 Free"}, {"gpt-6-luna-decision", "GPT 6 Luna Decision"}, {"gemini-decision-1", "Gemini Decision 1"}},
		"openrouter": {{"typesafe/jev-1.13", "Jev 1.13"}, {"typesafe/jev-1.14", "Jev 1.14"}, {"~typesafe/jev-latest", "Jev Latest"}},
		"subq":       {{"subq-snap-preview", "SubQ Snap (preview)"}},
		"typesafe":   {{"jev-latest", "Jev"}, {"jev-preview", "Jev Preview"}, {"jev-1.13.0", "Jev 1.13.0"}},
	},
}

func DeclaredChoice(adapter, kind, id string) bool {
	return slices.ContainsFunc(declaredChoices[kind][adapter], func(m namedModel) bool { return m.id == id })
}

func Choices(adapter, kind string) []provider.OutputMetadata {
	// Prefer source metadata when it has a real display name; the pinned names
	// fill in older ID-only rows and models on a separate API of a chat provider.
	rows := provider.SpecializedModels(CatalogProviderID(adapter), kind)
	for _, m := range declaredChoices[kind][adapter] {
		if i := slices.IndexFunc(rows, func(row provider.OutputMetadata) bool { return row.ID == m.id }); i >= 0 {
			if rows[i].Name == "" || rows[i].Name == m.id {
				rows[i].Name = m.name
			}
		} else {
			rows = append(rows, provider.OutputMetadata{ID: m.id, Name: m.name, Kind: kind})
		}
	}
	return rows
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
