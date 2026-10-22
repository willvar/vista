package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	modelcfg "vista/internal/model"
	"vista/internal/useragent"
)

const modelHeaderTimeout = 2 * time.Minute
const modelReadIdleTimeout = 2 * time.Minute
const maxModelErrorBytes = 128 << 10
const maxModelDisplayErrorBytes = 8 << 10

// ModelHTTPError keeps the upstream explanation as well as HTTP metadata.
// ModelTurn redacts request credentials before the error leaves the adapter.
type ModelHTTPError struct {
	Provider   string
	StatusCode int
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e *ModelHTTPError) Error() string {
	message := e.Provider + " stream reported an error"
	if e.StatusCode != 0 {
		message = fmt.Sprintf("%s HTTP %d", e.Provider, e.StatusCode)
	}
	if e.Code == "ModelDeprecated" {
		message += " (model retired by Provider; choose another model)"
	} else if e.Code != "" {
		message += " (" + e.Code + ")"
	}
	if e.RetryAfter > 0 {
		message += "; retry after " + e.RetryAfter.String()
	}
	if e.Message != "" {
		message += ": " + e.Message
	}
	return message
}

// ModelListHTTPError uses the same bounded diagnostics as inference, redacting
// discovery credentials before the upstream explanation reaches settings.
func ModelListHTTPError(response *http.Response, apiKey string, headers map[string]string) error {
	return modelErrorForDisplay(modelHTTPError("model list", response), modelcfg.Model{APIKey: apiKey, Headers: headers})
}

func modelHTTPError(provider string, response *http.Response) error {
	result := &ModelHTTPError{Provider: provider, StatusCode: response.StatusCode}
	if seconds, err := strconv.Atoi(strings.TrimSpace(response.Header.Get("Retry-After"))); err == nil && seconds > 0 && seconds <= 86400 {
		result.RetryAfter = time.Duration(seconds) * time.Second
	} else if date, err := http.ParseTime(response.Header.Get("Retry-After")); err == nil {
		if delay := time.Until(date); delay > 0 && delay <= 24*time.Hour {
			result.RetryAfter = delay
		}
	}
	// Error bodies are untrusted and may stream indefinitely. A short total
	// deadline and byte cap keep this diagnostic path from blocking a turn.
	done := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(io.LimitReader(response.Body, maxModelErrorBytes+1))
		done <- data
	}()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case data := <-done:
		if len(data) <= maxModelErrorBytes {
			result.Code = modelErrorCode(data)
			result.Message = modelErrorMessage(data)
			if errorEnvelopeOverflow(data) {
				result.Code = "context_length_exceeded"
			}
		}
	case <-timer.C:
		_ = response.Body.Close()
	}
	return result
}

func modelErrorCode(data []byte) string {
	var payload map[string]json.RawMessage
	if json.Unmarshal(data, &payload) != nil {
		return ""
	}
	if nested := payload["response"]; len(nested) > 0 {
		if code := modelErrorCode(nested); code != "" {
			return code
		}
	}
	if nested := payload["error"]; len(nested) > 0 {
		var object map[string]json.RawMessage
		if json.Unmarshal(nested, &object) == nil {
			for _, field := range []string{"code", "type", "status"} {
				if code := modelErrorIdentifier(object[field]); code != "" {
					return code
				}
			}
		}
	}
	for _, field := range []string{"code", "type", "status"} {
		if code := modelErrorIdentifier(payload[field]); code != "" {
			return code
		}
	}
	return ""
}

func modelErrorIdentifier(raw json.RawMessage) string {
	var code string
	if json.Unmarshal(raw, &code) != nil || len(code) > 128 || code == "error" {
		return ""
	}
	for _, r := range code {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return ""
		}
	}
	return code
}

func modelErrorMessage(data []byte) string {
	var payload map[string]json.RawMessage
	if json.Unmarshal(data, &payload) != nil {
		var message string
		if json.Unmarshal(data, &message) == nil {
			return strings.TrimSpace(message)
		}
		message = strings.TrimSpace(string(data))
		// HTML error pages are not actionable error messages.
		if strings.HasPrefix(message, "<") || strings.HasPrefix(message, "{") || strings.HasPrefix(message, "[") {
			return ""
		}
		return message
	}
	for _, field := range []string{"response", "error"} {
		if nested := payload[field]; len(nested) > 0 {
			if message := modelErrorMessage(nested); message != "" {
				return message
			}
		}
	}
	for _, field := range []string{"message", "detail", "error_description", "errorMessage"} {
		var message string
		if json.Unmarshal(payload[field], &message) == nil && strings.TrimSpace(message) != "" {
			return strings.TrimSpace(message)
		}
	}
	return ""
}

type modelDisplayError struct {
	error
	message string
}

func (e *modelDisplayError) Error() string { return e.message }
func (e *modelDisplayError) Unwrap() error { return e.error }

func modelErrorForDisplay(err error, model modelcfg.Model) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	secrets := []string{model.APIKey, model.AppCode, model.AWSAccessID, model.AWSSecret, model.AWSSession}
	for _, headers := range []map[string]string{model.Headers, model.GitLabHeaders} {
		for _, value := range headers {
			secrets = append(secrets, value)
			if parts := strings.Fields(value); len(parts) == 2 && (strings.EqualFold(parts[0], "Bearer") || strings.EqualFold(parts[0], "Basic")) {
				secrets = append(secrets, parts[1])
			}
		}
	}
	if model.OpenRouter != nil {
		for _, value := range model.OpenRouter.APIKeys {
			secrets = append(secrets, value)
		}
	}
	sort.SliceStable(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	var replacements []string
	for _, secret := range secrets {
		if secret != "" {
			replacements = append(replacements, secret, "[redacted]", url.QueryEscape(secret), "[redacted]")
		}
	}
	message = strings.NewReplacer(replacements...).Replace(message)
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, message)
	// Redact before truncating, so a credential crossing the boundary cannot
	// leave an unredacted prefix. Preserve the typed cause for overflow checks.
	if len(message) > maxModelDisplayErrorBytes {
		end := maxModelDisplayErrorBytes - len("…")
		for !utf8.RuneStart(message[end]) {
			end--
		}
		message = message[:end] + "…"
	}
	if message == err.Error() {
		return err
	}
	return &modelDisplayError{error: err, message: message}
}

var modelHTTPTransport = func() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = modelHeaderTimeout
	return transport
}()

var client = &http.Client{Transport: modelHTTPTransport, CheckRedirect: func(*http.Request, []*http.Request) error {
	return errors.New("model endpoint redirected")
}}

// A healthy stream may take longer than two minutes in total. Bound each
// blocking read instead of setting http.Client.Timeout on the whole turn.
func doStreamingRequest(request *http.Request, httpClient *http.Client, idle time.Duration) (*http.Response, error) {
	ctx, cancel := context.WithCancel(request.Context())
	response, err := useragent.Do(httpClient, request.Clone(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	response.Body = &modelIdleBody{source: response.Body, cancel: cancel, idle: idle}
	return response, nil
}

type modelIdleBody struct {
	source io.ReadCloser
	cancel context.CancelFunc
	idle   time.Duration
}

func (body *modelIdleBody) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return body.source.Read(buffer)
	}
	var mutex sync.Mutex
	reading := true
	expired := false
	timer := time.AfterFunc(body.idle, func() {
		mutex.Lock()
		if reading {
			expired = true
			body.cancel()
		}
		mutex.Unlock()
	})
	count, err := body.source.Read(buffer)
	mutex.Lock()
	reading = false
	timer.Stop()
	timedOut := expired
	mutex.Unlock()
	if timedOut {
		return count, fmt.Errorf("model stream idle timeout: %w", context.DeadlineExceeded)
	}
	return count, err
}

func (body *modelIdleBody) Close() error {
	body.cancel()
	return body.source.Close()
}
