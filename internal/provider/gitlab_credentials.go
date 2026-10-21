package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	modelcfg "vista/internal/model"
)

var gitlabAuthClient = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
	return errors.New("GitLab authorization endpoint redirected")
}}

// GitLab's PAT is exchanged for a short-lived AI Gateway token.
// Only the returned gateway material is sent to the Worker.
func resolveGitLabForRun(ctx context.Context, model modelcfg.Model, selected string) (modelcfg.Model, string, error) {
	if model.APIKey == "" || model.GitLabInstanceURL == "" {
		return modelcfg.Model{}, "", errors.New("GitLab instance and token are required")
	}
	body := []byte(`{"feature_flags":{"DuoAgentPlatformNext":true}}`)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(model.GitLabInstanceURL, "/")+"/api/v4/ai/third_party_agents/direct_access", bytes.NewReader(body))
	if err != nil {
		return modelcfg.Model{}, "", errors.New("invalid GitLab authorization endpoint")
	}
	request.Header.Set("Authorization", "Bearer "+model.APIKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := gitlabAuthClient.Do(request)
	if err != nil {
		return modelcfg.Model{}, "", errors.New("GitLab direct access token request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return modelcfg.Model{}, "", errors.New("GitLab direct access token request was rejected")
	}
	var result struct {
		Token     string            `json:"token"`
		ExpiresAt json.RawMessage   `json:"expires_at"`
		Headers   map[string]string `json:"headers"`
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (16<<10)+1))
	if err != nil || len(raw) > 16<<10 || json.Unmarshal(raw, &result) != nil || result.Token == "" || len(result.Token) > 4096 {
		return modelcfg.Model{}, "", errors.New("invalid GitLab direct access token response")
	}
	if len(result.Headers) > 16 {
		return modelcfg.Model{}, "", errors.New("GitLab direct access headers exceed limits")
	}
	clean := make(map[string]string, len(result.Headers))
	total := 0
	for name, value := range result.Headers {
		if !validGitLabHeaderName(name) || !validGitLabHeaderValue(value) {
			return modelcfg.Model{}, "", errors.New("invalid GitLab direct access header")
		}
		switch strings.ToLower(name) {
		case "authorization", "x-api-key", "content-type", "content-length", "host", "connection", "transfer-encoding", "user-agent":
			continue
		}
		total += len(name) + len(value)
		if total > 2048 {
			return modelcfg.Model{}, "", errors.New("GitLab direct access headers exceed limits")
		}
		clean[name] = value
	}
	model.APIKey = result.Token
	model.CredentialExpiresAt = gitlabTokenExpiry(result.ExpiresAt)
	model.GitLabHeaders = clean
	model.GitLabInstanceURL = ""
	return model, selected, nil
}

func validGitLabHeaderName(name string) bool {
	if len(name) == 0 || len(name) > 80 {
		return false
	}
	for _, ch := range name {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", ch) {
			continue
		}
		return false
	}
	return true
}

func validGitLabHeaderValue(value string) bool {
	if len(value) > 512 {
		return false
	}
	for _, ch := range value {
		if ch < 0x20 && ch != '\t' || ch == 0x7f {
			return false
		}
	}
	return true
}

// GitLab forwards the gateway's expires_at. Accept epoch seconds and RFC3339;
// a missing expiry uses the credential cache's short lifetime.
func gitlabTokenExpiry(raw json.RawMessage) time.Time {
	var seconds int64
	if json.Unmarshal(raw, &seconds) == nil && seconds > 0 {
		return time.Unix(seconds, 0)
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		expiry, _ := time.Parse(time.RFC3339, text)
		return expiry
	}
	return time.Time{}
}
