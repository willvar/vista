package provider

import (
	"net/http"
	"strings"

	"vista/internal/copilotauth"
	modelcfg "vista/internal/model"
)

// The pinned Copilot snapshot is OpenAI-compatible metadata. The locked
// OpenCode plugin selects a different wire protocol for several model families.
func copilotProtocol(id string) string {
	switch {
	case strings.HasPrefix(id, "claude-"):
		return "anthropic"
	case strings.HasPrefix(id, "gemini-"), strings.HasPrefix(id, "grok-"), strings.HasPrefix(id, "mai-code-"):
		return "openai-responses"
	case strings.HasPrefix(id, "gpt-") && id != "gpt-5-mini":
		return "openai-responses"
	default:
		return "openai-chat"
	}
}

func copilotHeaders(request *http.Request, model modelcfg.Model, messages []ChatMessage) {
	if model.ProviderID != "github-copilot" {
		return
	}
	copilotauth.SetAPIHeaders(request.Header)
	for _, message := range messages {
		if len(message.Images) > 0 {
			request.Header.Set("Copilot-Vision-Request", "true")
			break
		}
	}
	request.Header.Set("Openai-Intent", "conversation-edits")
	request.Header.Set("X-GitHub-Api-Version", "2026-08-01")
	interaction := model.InteractionType
	if interaction == "" {
		interaction = "conversation-agent"
	}
	request.Header.Set("X-Interaction-Type", interaction)
	if model.SessionID != "" {
		request.Header.Set("X-Interaction-Id", model.SessionID)
	}
	initiator := "agent"
	if interaction == "conversation-agent" && len(messages) > 0 && messages[len(messages)-1].Role == "user" {
		initiator = "user"
	}
	request.Header.Set("x-initiator", initiator)
}
