package generation

import (
	"errors"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"unicode"

	"vista/internal/model"
)

const SourceRevision = "e13cf15ece9bbd969b7362927c315362c90c97ff"

// Spec identifies a wire dialect, not a promise of access to a model. Only
// registered operations are offered in settings; a chat API cannot fall back
// to a different service when a generation request fails.
type Spec struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Base  string   `json:"baseUrl"`
	Kinds []string `json:"kinds"`
	Frame bool     `json:"frame"`
}

var specs = []Spec{
	{"openai", "OpenAI", "https://api.openai.com/v1", []string{model.ImageOutput, model.DecisionOutput}, false},
	{"openai-codex", "OpenAI Codex", "", []string{model.ImageOutput}, false},
	{"openrouter", "OpenRouter", "https://openrouter.ai/api/v1", []string{model.ImageOutput, model.VideoOutput, model.DecisionOutput}, true},
	{"google", "Google Gemini / Veo", "https://generativelanguage.googleapis.com/v1beta", []string{model.ImageOutput, model.VideoOutput, model.DecisionOutput}, true},
	{"xai", "xAI", "https://api.x.ai/v1", []string{model.ImageOutput, model.VideoOutput}, true},
	{"deepinfra", "DeepInfra", "https://api.deepinfra.com/v1/openai", []string{model.ImageOutput, model.VideoOutput}, false},
	{"litellm", "LiteLLM", "", []string{model.ImageOutput}, false},
	{"clawrouter", "ClawRouter", "https://clawrouter.openclaw.ai/v1", []string{model.ImageOutput, model.DecisionOutput}, false},
	{"microsoft-foundry", "Microsoft Foundry", "", []string{model.ImageOutput}, false},
	{"comfy", "ComfyUI", "http://127.0.0.1:8188", []string{model.ImageOutput, model.VideoOutput}, true},
	{"vydra", "Vydra", "https://www.vydra.ai/api/v1", []string{model.ImageOutput, model.VideoOutput}, false},
	{"fal", "fal", "https://queue.fal.run", []string{model.ImageOutput, model.VideoOutput}, true},
	{"alibaba", "Alibaba Model Studio", "https://dashscope-intl.aliyuncs.com", []string{model.VideoOutput}, true},
	{"qwen", "Qwen / DashScope", "https://dashscope-intl.aliyuncs.com", []string{model.VideoOutput}, true},
	{"byteplus", "BytePlus", "https://ark.ap-southeast.bytepluses.com/api/v3", []string{model.VideoOutput}, true},
	{"kie", "Kie AI", "https://api.kie.ai", []string{model.VideoOutput}, false},
	{"pixverse", "PixVerse", "https://app-api.pixverse.ai/openapi/v2", []string{model.VideoOutput}, true},
	{"minimax", "MiniMax", "https://api.minimax.io", []string{model.ImageOutput, model.VideoOutput}, true},
	{"minimax-portal", "MiniMax Portal", "https://api.minimax.io", []string{model.ImageOutput, model.VideoOutput}, true},
	{"novita", "Novita", "https://api.novita.ai", []string{model.VideoOutput}, true},
	{"runway", "Runway", "https://api.dev.runwayml.com", []string{model.VideoOutput}, true},
	{"together", "Together AI", "https://api.together.xyz", []string{model.VideoOutput}, true},
	{"zai", "Z.AI", "https://api.z.ai/api/paas/v4", []string{model.VideoOutput}, true},
	{"baseten", "Baseten", "https://inference.baseten.co/v1", []string{model.DecisionOutput}, false},
	{"levanto", "Levanto Sage", "https://sage.levanto.ai", []string{model.DecisionOutput}, false},
	{"liquid-ai", "Liquid AI Decisions", "https://api.liquid.ai/decisions/v1", []string{model.DecisionOutput}, false},
	{"onnx", "ONNX (local)", "http://127.0.0.1:8189/v1", []string{model.DecisionOutput}, false},
	{"opencode", "OpenCode", "https://opencode.ai/inference/systemone/v1", []string{model.DecisionOutput}, false},
	{"subq", "SubQ", "https://api.subq.ai/v1", []string{model.DecisionOutput}, false},
	{"typesafe", "TypeSafe", "https://api.typesafe.ai/v1", []string{model.DecisionOutput}, false},
}

func Specs() []Spec { return slices.Clone(specs) }
func Find(id, kind string) (Spec, bool) {
	for _, s := range specs {
		if s.ID == id && slices.Contains(s.Kinds, kind) {
			return s, true
		}
	}
	return Spec{}, false
}

// CatalogProviderID maps a wire adapter to its metadata roster.
func CatalogProviderID(adapter string) string {
	switch adapter {
	case "openai-codex":
		return "openai"
	case "together":
		return "togetherai"
	case "minimax-portal":
		return "minimax"
	case "novita":
		return "novita-ai"
	}
	return adapter
}

func Endpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || len(raw) > 2048 || strings.ContainsAny(u.EscapedPath(), "\\\r\n") {
		return nil, errors.New("enter a valid HTTP(S) service address without credentials or query parameters")
	}
	return u, nil
}

func ValidateModelID(id string) bool {
	if id == "" || len(id) > 200 || strings.IndexFunc(id, unicode.IsSpace) >= 0 || strings.ContainsAny(id, "?#%\\\x00") {
		return false
	}
	for _, part := range strings.Split(id, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// Local service addresses must be literal loopback addresses, not proxy-resolved names.
func IsLoopbackService(u *url.URL) bool {
	ip, err := netip.ParseAddr(u.Hostname())
	return err == nil && ip.Unmap().IsLoopback()
}
