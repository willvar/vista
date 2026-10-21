package copilotauth

import (
	"net/http"

	"vista/internal/useragent"
)

// Native Copilot identities share the bundled UA version; the header layout
// follows this wire profile:
// https://github.com/can1357/oh-my-pi/blob/602b6c812fa9ef774f359f1e399a09d30ee2eaca/packages/catalog/src/wire/github-copilot.ts
const UserAgent = useragent.CopilotUserAgent
const oauthUserAgent = "copilot-developer-action/0.0.1"

// SetAPIHeaders is shared by account lookup, model discovery, and inference.
// Request-specific intent, interaction, and initiator headers stay with callers.
func SetAPIHeaders(headers http.Header) {
	headers.Set("User-Agent", UserAgent)
	headers.Set("Editor-Version", UserAgent)
	headers.Set("Copilot-Integration-Id", "copilot-developer-cli")
	headers.Set("Copilot-Harness-Id", "copilot-sdk")
}
