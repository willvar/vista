package provider

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	modelcfg "vista/internal/model"
)

// These headers are the stable part of the Codex Responses transport needed
// for a configured bearer token or a compatible proxy. OAuth refresh and
// account storage remain outside the model request boundary.
const (
	codexBaseURL       = "https://chatgpt.com/backend-api"
	codexClientVersion = "0.159.0"
)

func codexResponsesEndpoint(base *url.URL) string {
	endpoint := strings.TrimRight(base.String(), "/")
	if strings.HasSuffix(endpoint, "/codex/responses") {
		return endpoint
	}
	if strings.HasSuffix(endpoint, "/codex") {
		return endpoint + "/responses"
	}
	return endpoint + "/codex/responses"
}

func setCodexHeaders(request *http.Request, model modelcfg.Model) {
	if model.ResponsesRoute != modelcfg.ResponsesRouteCodex {
		return
	}
	request.Header.Set("OpenAI-Beta", "responses=experimental")
	request.Header.Set("originator", "omp")
	request.Header.Set("version", codexClientVersion)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("User-Agent", "vista")
	if accountID := codexAccountID(model.APIKey); accountID != "" {
		request.Header.Set("chatgpt-account-id", accountID)
	}
}

// Codex OAuth access tokens carry the account ID in a namespaced JWT claim.
// API keys and opaque proxy tokens simply yield no account header.
func codexAccountID(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Auth struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return strings.TrimSpace(claims.Auth.AccountID)
}
