package provider

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// CustomProtocols lists the API formats available to custom connections.
// Platform-specific adapters remain owned by their built-in providers.
func CustomProtocols() []string {
	return []string{"openai-chat", "openai-responses", "anthropic", "gemini", "cohere"}
}

func IsCustomProtocol(protocol string) bool {
	return slices.Contains(CustomProtocols(), protocol)
}

func ValidateCustomAuthentication(authentication, key string) error {
	switch authentication {
	case "none":
		if key != "" {
			return errors.New("no authentication does not accept an API key")
		}
	case "bearer", "x-api-key", "x-goog-api-key":
		if key == "" {
			return errors.New("an API key is required for the selected authentication method")
		}
	default:
		return errors.New("choose a supported authentication method")
	}
	return nil
}

// ApplyCustomRequestHeaders is shared by discovery and inference. Authentication
// is explicit and independent of the API format; custom headers apply last.
func ApplyCustomRequestHeaders(headers http.Header, authentication, key string, overrides map[string]string) {
	for _, name := range []string{"Authorization", "x-api-key", "x-goog-api-key"} {
		headers.Del(name)
	}
	switch authentication {
	case "bearer":
		headers.Set("Authorization", "Bearer "+key)
	case "x-api-key", "x-goog-api-key":
		headers.Set(authentication, key)
	}
	for name, value := range overrides {
		headers.Set(name, value)
	}
}

func validCustomBaseURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Scheme == "http" || u.Scheme == "https")
}

func ValidateCustomHeaders(headers map[string]string) error {
	if len(headers) > 32 {
		return errors.New("configure at most 32 custom headers")
	}
	seen := map[string]bool{}
	size := 0
	for name, value := range headers {
		key := http.CanonicalHeaderKey(name)
		if !httpguts.ValidHeaderFieldName(name) || !httpguts.ValidHeaderFieldValue(value) || seen[key] {
			return errors.New("custom headers contain an invalid or duplicate name or value")
		}
		if slices.Contains([]string{"Host", "Content-Length", "Transfer-Encoding", "Connection"}, key) {
			return errors.New("custom headers cannot override HTTP routing or framing")
		}
		seen[key] = true
		size += len(name) + len(value)
	}
	if size > 8192 {
		return errors.New("custom headers exceed 8 KiB")
	}
	return nil
}

// CustomBaseURL preserves the user's path prefix. A bare host gets the selected
// API's conventional version; an explicit path is never rewritten to another API.
func CustomBaseURL(base, protocol string) string {
	base = strings.TrimRight(base, "/")
	u, err := url.Parse(base)
	if err != nil || u.Path != "" {
		return base
	}
	u.Path = "/v1"
	switch protocol {
	case "gemini":
		u.Path = "/v1beta"
	case "cohere":
		u.Path = "/v2"
	}
	return u.String()
}
