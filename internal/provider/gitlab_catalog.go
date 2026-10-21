package provider

import "strings"

// GitLab's Duo model names select a proxy protocol and an upstream model ID.
// These are the text models in the pinned GitLab inventory.
var gitlabModelIDs = map[string]string{
	"duo-chat-fable-5":       "claude-fable-5",
	"duo-chat-fable-5-1":     "claude-fable-5-1",
	"duo-chat-haiku-4-5":     "claude-haiku-4-5-20251001",
	"duo-chat-opus-4-5":      "claude-opus-4-5-20251101",
	"duo-chat-opus-4-6":      "claude-opus-4-6",
	"duo-chat-opus-4-7":      "claude-opus-4-7",
	"duo-chat-opus-4-8":      "claude-opus-4-8",
	"duo-chat-opus-5":        "claude-opus-5",
	"duo-chat-opus-5-5":      "claude-opus-5-5",
	"duo-chat-sonnet-4-5":    "claude-sonnet-4-5-20250929",
	"duo-chat-sonnet-4-6":    "claude-sonnet-4-6",
	"duo-chat-sonnet-5":      "claude-sonnet-5",
	"duo-chat-gpt-5-1":       "gpt-5.1-2025-11-13",
	"duo-chat-gpt-5-2":       "gpt-5.2-2025-12-11",
	"duo-chat-gpt-5-2-codex": "gpt-5.2-codex",
	"duo-chat-gpt-5-3-codex": "gpt-5.3-codex",
	"duo-chat-gpt-5-4":       "gpt-5.4-2026-03-05",
	"duo-chat-gpt-5-4-mini":  "gpt-5.4-mini",
	"duo-chat-gpt-5-4-nano":  "gpt-5.4-nano",
	"duo-chat-gpt-5-5":       "gpt-5.5-2026-04-23",
	"duo-chat-gpt-5-6-luna":  "gpt-5.6-luna",
	"duo-chat-gpt-5-6-sol":   "gpt-5.6-sol",
	"duo-chat-gpt-5-6-terra": "gpt-5.6-terra",
	"duo-chat-gpt-5-codex":   "gpt-5-codex",
	"duo-chat-gpt-5-mini":    "gpt-5-mini-2025-08-07",
	"duo-chat-gpt-6-astra":   "gpt-6-astra",
	"duo-chat-gpt-6-luna":    "gpt-6-luna",
	"duo-chat-gpt-6-sol":     "gpt-6-sol",
}

func gitlabModelRoute(id string) (protocol, wireID string) {
	wireID = gitlabModelIDs[id]
	if wireID == "" {
		return "", ""
	}
	if strings.HasPrefix(wireID, "claude-") {
		return "anthropic", wireID
	}
	switch id {
	case "duo-chat-gpt-5-2-codex", "duo-chat-gpt-5-3-codex", "duo-chat-gpt-5-codex", "duo-chat-gpt-5-6-luna", "duo-chat-gpt-5-6-sol", "duo-chat-gpt-5-6-terra", "duo-chat-gpt-6-astra", "duo-chat-gpt-6-luna", "duo-chat-gpt-6-sol":
		return "openai-responses", wireID
	default:
		return "openai-chat", wireID
	}
}
