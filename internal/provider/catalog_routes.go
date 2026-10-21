package provider

import (
	"fmt"
	"strings"
)

func resolveZAIRoute(state *catalogResolveState) error {
	model := &state.config
	switch model.Route {
	case "", "chat":
	case "messages":
		model.Provider = "anthropic"
		if model.BaseURL == "" {
			model.BaseURL = "https://api.z.ai/api/anthropic/v1"
		}
	case "responses":
		model.Provider = "openai-responses"
		if model.BaseURL == "" {
			model.BaseURL = "https://api.z.ai/api/v1"
		}
	default:
		return fmt.Errorf("catalog model %q has unsupported ZAI coding-plan route %q", model.Key, model.Route)
	}

	return nil
}

func resolveMoonshotRoute(state *catalogResolveState) error {
	model := &state.config
	host := "https://api.moonshot.ai"
	if model.ProviderID == "moonshotai-cn" {
		host = "https://api.moonshot.cn"
	}
	switch model.Route {
	case "", "chat":
	case "messages":
		model.Provider = "anthropic"
		if model.BaseURL == "" {
			model.BaseURL = host + "/anthropic/v1"
		}
	case "responses":
		model.Provider = "openai-responses"
		if model.BaseURL == "" {
			model.BaseURL = host + "/v1"
		}
	default:
		return fmt.Errorf("catalog model %q has unsupported Moonshot route %q", model.Key, model.Route)
	}

	return nil
}

func resolveAlibabaRoute(state *catalogResolveState) error {
	model := &state.config
	host := "https://dashscope-intl.aliyuncs.com"
	if model.ProviderID == "alibaba-cn" {
		host = "https://dashscope.aliyuncs.com"
	}
	if model.Region != "" {
		if model.WorkspaceID != "" {
			host = "https://" + model.WorkspaceID + "." + model.Region + ".maas.aliyuncs.com"
		} else {
			hosts := map[string]string{
				"ap-southeast-1": "dashscope-intl.aliyuncs.com",
				"cn-beijing":     "dashscope.aliyuncs.com",
				"cn-hongkong":    "cn-hongkong.dashscope.aliyuncs.com",
				"us-east-1":      "dashscope-us.aliyuncs.com",
			}
			if known := hosts[model.Region]; known != "" {
				host = "https://" + known
			}
		}
		if model.BaseURL == "" && (model.Route == "" || model.Route == "chat") {
			model.BaseURL = host + "/compatible-mode/v1"
		}
	}
	switch model.Route {
	case "", "chat":
	case "messages":
		model.Provider = "anthropic"
		if model.BaseURL == "" {
			model.BaseURL = host + "/apps/anthropic/v1"
		}
	case "responses":
		model.Provider = "openai-responses"
		if model.BaseURL == "" {
			model.BaseURL = host + "/compatible-mode/v1"
		}
	default:
		return fmt.Errorf("catalog model %q has unsupported Alibaba route %q", model.Key, model.Route)
	}

	return nil
}

func resolveMiniMaxRoute(state *catalogResolveState) error {
	model := &state.config
	host := "https://api.minimax.io"
	if strings.Contains(model.ProviderID, "-cn") {
		host = "https://api.minimax.cn"
	}
	switch model.Route {
	case "", "messages":
	case "chat":
		model.Provider = "openai-chat"
		if model.BaseURL == "" {
			model.BaseURL = host + "/v1"
		}
	case "responses":
		model.Provider = "openai-responses"
		if model.BaseURL == "" {
			model.BaseURL = host + "/v1"
		}
	default:
		return fmt.Errorf("catalog model %q has unsupported MiniMax route %q", model.Key, model.Route)
	}

	return nil
}

func resolveMetaRoute(state *catalogResolveState) error {
	model := &state.config
	switch model.Route {
	case "", "responses":
	case "chat":
		model.Provider = "openai-chat"
	case "messages":
		model.Provider = "anthropic"
	default:
		return fmt.Errorf("catalog model %q has unsupported Meta route %q", model.Key, model.Route)
	}

	return nil
}

func resolveXAIRoute(state *catalogResolveState) error {
	model := &state.config
	switch model.Route {
	case "", "responses":
	case "chat":
		model.Provider = "openai-chat"
	default:
		return fmt.Errorf("catalog model %q has unsupported xAI route %q", model.Key, model.Route)
	}

	return nil
}
