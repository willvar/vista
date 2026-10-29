package mcpclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"
)

// SearchConfig is shared by startup warming and the actual search call.
func SearchConfig(name, endpoint, token string) (Config, bool) {
	if name != "exa" && name != "firecrawl" && name != "parallel" {
		return Config{}, false
	}
	if name == "exa" && token != "" {
		if u, err := url.Parse(endpoint); err == nil {
			query := u.Query()
			query.Set("exaApiKey", token)
			u.RawQuery = query.Encode()
			endpoint, token = u.String(), ""
		}
	}
	return Config{ID: "search", Name: name, Transport: "http", Endpoint: endpoint, Token: token}, true
}

// OAuthSnapshot keeps an execution's authorization independent of subsequent
// edits/deletion. Token refreshes update this private version's copy.
func OAuthSnapshot(dir string, config Config) (string, func(string) error) {
	if !config.OAuth {
		return "", nil
	}
	value, err := readOAuth(oauthPath(dir, config.ID), config.Endpoint)
	if err != nil {
		return "unauthorized", nil
	}
	version := oauthGeneration(value)
	value.Generation, value.OriginPath = version, oauthPath(dir, config.ID)
	return version, func(destination string) error {
		return writeOAuth(oauthPath(destination, config.ID), value)
	}
}

func oauthGeneration(value oauthCredential) string {
	if value.Generation != "" {
		return value.Generation
	}
	data, _ := json.Marshal(value)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func RedactError(message string, config Config) string {
	secrets := []string{config.Token, config.ClientSecret}
	for _, value := range config.Headers {
		secrets = append(secrets, value)
	}
	if endpoint, err := url.Parse(config.Endpoint); err == nil {
		for _, values := range endpoint.Query() {
			secrets = append(secrets, values...)
		}
	}
	for _, value := range secrets {
		if value != "" {
			message = strings.ReplaceAll(message, value, "[redacted]")
			message = strings.ReplaceAll(message, url.QueryEscape(value), "[redacted]")
		}
	}
	if runes := []rune(message); len(runes) > 2048 {
		message = string(runes[:2048]) + "…"
	}
	return message
}
