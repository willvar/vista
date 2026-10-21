package provider

import (
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"

	modelcfg "vista/internal/model"
)

func validateOpenRouterOptions(providerID, protocol string, options *modelcfg.OpenRouterOptions) error {
	if options == nil {
		return nil
	}
	if (providerID != "openrouter" && providerID != "standardcompute") || protocol != "openai-chat" {
		return errors.New("openRouter options require a pinned OpenRouter Chat model")
	}
	encoded, err := json.Marshal(options)
	if err != nil || len(encoded) > 2048 {
		return errors.New("openRouter options exceed 2 KiB")
	}
	if len(options.AppName) > 128 || strings.TrimSpace(options.AppName) != options.AppName || hasControl(options.AppName) {
		return errors.New("invalid OpenRouter appName")
	}
	if options.AppURL != "" {
		u, err := url.Parse(options.AppURL)
		if err != nil || len(options.AppURL) > 2048 || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || (u.Scheme != "https" && !(u.Scheme == "http" && localLoopback(u.Hostname()))) {
			return errors.New("OpenRouter appUrl requires HTTPS or loopback HTTP without credentials, query, or fragment")
		}
	}
	if len(options.APIKeys) > 8 {
		return errors.New("OpenRouter provider API keys exceed eight entries")
	}
	for name, key := range options.APIKeys {
		if err := validateOpenRouterNames([]string{name}); err != nil || key == "" || len(key) > 4096 || strings.TrimSpace(key) != key || hasControl(key) {
			return errors.New("invalid OpenRouter provider API key")
		}
	}
	if err := validateOpenRouterNames(options.Models); err != nil {
		return err
	}
	if len(options.User) > 128 || strings.TrimSpace(options.User) != options.User || hasControl(options.User) {
		return errors.New("invalid OpenRouter user")
	}
	if len(options.Plugins) > 8 {
		return errors.New("OpenRouter plugins exceed eight entries")
	}
	seenPlugins := map[string]bool{}
	for _, plugin := range options.Plugins {
		if seenPlugins[plugin.ID] {
			return errors.New("duplicate OpenRouter plugin")
		}
		seenPlugins[plugin.ID] = true
		switch plugin.ID {
		case "file-parser":
			if len(plugin.AllowedModels) > 0 || plugin.MaxFiles != nil && (*plugin.MaxFiles < 1 || *plugin.MaxFiles > 16) {
				return errors.New("invalid OpenRouter file-parser options")
			}
			if plugin.PDF != nil && (len(plugin.PDF.Engine) > 64 || hasControl(plugin.PDF.Engine) || strings.TrimSpace(plugin.PDF.Engine) != plugin.PDF.Engine) {
				return errors.New("invalid OpenRouter PDF engine")
			}
		case "auto-router":
			if plugin.MaxFiles != nil || plugin.PDF != nil {
				return errors.New("invalid OpenRouter auto-router options")
			}
			if err := validateOpenRouterNames(plugin.AllowedModels); err != nil {
				return err
			}
		case "moderation", "response-healing":
			if plugin.MaxFiles != nil || plugin.PDF != nil || len(plugin.AllowedModels) > 0 {
				return errors.New("invalid OpenRouter plugin options")
			}
		default:
			return errors.New("unknown OpenRouter plugin")
		}
	}
	routing := options.Provider
	if routing != nil {
		if routing.DataCollection != "" && !slices.Contains([]string{"allow", "deny"}, routing.DataCollection) || routing.Sort != "" && !slices.Contains([]string{"price", "throughput", "latency"}, routing.Sort) {
			return errors.New("invalid OpenRouter provider routing mode")
		}
		for _, names := range [][]string{routing.Order, routing.Only, routing.Ignore, routing.Quantizations} {
			if err := validateOpenRouterNames(names); err != nil {
				return err
			}
		}
		if routing.MaxPrice != nil {
			for _, value := range []json.RawMessage{routing.MaxPrice.Prompt, routing.MaxPrice.Completion, routing.MaxPrice.Image, routing.MaxPrice.Audio, routing.MaxPrice.Request} {
				if len(value) == 0 {
					continue
				}
				if string(value) == "null" {
					return errors.New("invalid OpenRouter max_price value")
				}
				var number float64
				if value[0] == '"' {
					var stringValue string
					if json.Unmarshal(value, &stringValue) != nil || len(stringValue) > 32 || strings.TrimSpace(stringValue) != stringValue {
						return errors.New("invalid OpenRouter max_price value")
					}
					var err error
					number, err = strconv.ParseFloat(stringValue, 64)
					if err != nil {
						return errors.New("invalid OpenRouter max_price value")
					}
				} else if json.Unmarshal(value, &number) != nil {
					return errors.New("invalid OpenRouter max_price value")
				}
				if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || number > 1_000_000 {
					return errors.New("OpenRouter max_price is outside the supported range")
				}
			}
		}
	}
	if reasoning := options.Reasoning; reasoning != nil {
		if reasoning.Effort != "" && !slices.Contains([]string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}, reasoning.Effort) {
			return errors.New("invalid OpenRouter reasoning effort")
		}
		if reasoning.MaxTokens != nil && (*reasoning.MaxTokens < -1 || *reasoning.MaxTokens > 1_000_000) {
			return errors.New("OpenRouter reasoning max_tokens is outside the supported range")
		}
	}
	return nil
}

func validateOpenRouterNames(names []string) error {
	if len(names) > 8 {
		return errors.New("OpenRouter routing list exceeds eight entries")
	}
	for _, name := range names {
		if name == "" || len(name) > 200 || strings.TrimSpace(name) != name || hasControl(name) {
			return errors.New("invalid OpenRouter model or provider name")
		}
	}
	return nil
}

func hasControl(value string) bool {
	for _, ch := range value {
		if ch < 0x20 || ch == 0x7f {
			return true
		}
	}
	return false
}
