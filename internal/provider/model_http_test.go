package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	modelcfg "vista/internal/model"
)

func TestModelStreamCanOutlastItsReadIdleLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for range 4 {
			_, _ = io.WriteString(w, "part")
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	idle := 300 * time.Millisecond
	response, err := doStreamingRequest(request, client, idle)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	start := time.Now()
	data, err := io.ReadAll(response.Body)
	if err != nil || string(data) != "partpartpartpart" || time.Since(start) < idle {
		t.Fatalf("healthy long stream: %q, elapsed=%s, err=%v", data, time.Since(start), err)
	}
}

func TestModelStreamIdleTimeoutAndCallerCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "first")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	for _, check := range []struct {
		name   string
		idle   time.Duration
		cancel bool
		want   error
	}{
		{name: "idle", idle: 80 * time.Millisecond, want: context.DeadlineExceeded},
		{name: "caller", idle: time.Second, cancel: true, want: context.Canceled},
	} {
		t.Run(check.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			response, err := doStreamingRequest(request, client, check.idle)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			first := make([]byte, 5)
			if _, err := io.ReadFull(response.Body, first); err != nil || string(first) != "first" {
				t.Fatalf("first fragment: %q, err=%v", first, err)
			}
			if check.cancel {
				cancel()
			}
			_, err = io.ReadAll(response.Body)
			if !errors.Is(err, check.want) {
				t.Fatalf("stream end: got %v, want %v", err, check.want)
			}
		})
	}
}

func TestModelHTTPFailureIsNotAutomaticallyReplayed(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				w.WriteHeader(status)
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: "openai-chat", ID: "mock", BaseURL: server.URL, APIKey: "key"}
			_, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if err == nil || !strings.Contains(err.Error(), strconv.Itoa(status)) || requests != 1 {
				t.Fatalf("HTTP failure was replayed or hidden: requests=%d, err=%v", requests, err)
			}
		})
	}
}

func TestProviderHTTPFailuresPreserveExplanationAndRedactCredentials(t *testing.T) {
	for _, protocol := range []string{"openai-chat", "openai-responses", "anthropic", "gemini", "mistral", "cohere", "vercel-gateway"} {
		t.Run(protocol, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":{"code":"insufficient_quota","message":"Quota exhausted for fixture-api-key.","request":"private request body"}}`)
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: protocol, ID: "mock", BaseURL: server.URL, APIKey: "fixture-api-key"}
			_, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			var failure *ModelHTTPError
			if !errors.As(err, &failure) || failure.StatusCode != 429 || failure.Code != "insufficient_quota" || failure.RetryAfter != 3*time.Second || requests != 1 {
				t.Fatalf("HTTP failure metadata: requests=%d failure=%+v err=%v", requests, failure, err)
			}
			if !strings.Contains(err.Error(), "Quota exhausted for [redacted].") || strings.Contains(err.Error(), model.APIKey) || strings.Contains(err.Error(), "private request body") {
				t.Fatalf("error explanation missing or credentials/request leaked: %v", err)
			}
		})
	}
}

func TestModelHTTPErrorIgnoresOversizedBodiesAndInvalidCodes(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"not an identifier\n"}}`,
		`{"error":{"code":"rate_limit_error","message":"` + strings.Repeat("x", maxModelErrorBytes) + `"}}`,
	} {
		response := &http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
		var failure *ModelHTTPError
		if err := modelHTTPError("model", response); !errors.As(err, &failure) || failure.Code != "" {
			t.Fatalf("invalid or oversized code was retained: %+v, %v", failure, err)
		}
	}
	if code := modelErrorCode([]byte(`{"error":{"status":"RESOURCE_EXHAUSTED"}}`)); code != "RESOURCE_EXHAUSTED" {
		t.Fatalf("Gemini status code missing: %q", code)
	}
}

func TestModelDeprecatedErrorRetainsAdviceAndUpstreamMessage(t *testing.T) {
	response := &http.Response{StatusCode: 410, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"ModelDeprecated","message":"This model was retired on October 1."}}`))}
	err := modelHTTPError("model", response)
	var failure *ModelHTTPError
	if !errors.As(err, &failure) || failure.Code != "ModelDeprecated" || !strings.Contains(err.Error(), "choose another model") || !strings.Contains(err.Error(), "retired on October 1") {
		t.Fatalf("deprecated error: %+v", err)
	}
}

func TestModelErrorsPreserveProviderTypesAndMessages(t *testing.T) {
	for _, tc := range []struct {
		name, body, code, message string
	}{
		{"zen", `{"type":"error","error":{"type":"FreeTierError","message":"OpenCode's free tier can only be used from within OpenCode."}}`, "FreeTierError", "OpenCode's free tier can only be used from within OpenCode."},
		{"responses", `{"type":"response.failed","response":{"error":{"code":"invalid_api_key","message":"Your API key was revoked."}}}`, "invalid_api_key", "Your API key was revoked."},
		{"gemini", `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"This API is disabled for the project."}}`, "PERMISSION_DENIED", "This API is disabled for the project."},
		{"relay", `{"error":{"code":"channel_unavailable","message":"No channels are available for this model.","request":{"prompt":"private prompt"}}}`, "channel_unavailable", "No channels are available for this model."},
		{"string_error", `{"error":"The upstream account is suspended."}`, "", "The upstream account is suspended."},
		{"plain_text", "  Gateway access denied.\n", "", "Gateway access denied."},
		{"html", "<!DOCTYPE html><html><body>Challenge page</body></html>", "", ""},
		{"no_explanation", `{"type":"error","request":{"prompt":"private prompt"}}`, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body))}
			for _, err := range []error{modelHTTPError("model", response), modelStreamError("model", []byte(tc.body))} {
				var failure *ModelHTTPError
				if !errors.As(err, &failure) || failure.Code != tc.code || failure.Message != tc.message {
					t.Fatalf("upstream details changed: %+v, %v", failure, err)
				}
				if tc.message != "" && !strings.Contains(err.Error(), tc.message) {
					t.Fatalf("upstream explanation hidden: %v", err)
				}
			}
		})
	}
}

func TestDisplayedModelErrorsStayBoundedAndPreserveTheirCause(t *testing.T) {
	model := modelcfg.Model{APIKey: "fixture-key", AppCode: "fixture/token+value", Headers: map[string]string{"Authorization": "Bearer fixture-key-long", "X-Custom-Auth": "custom-token"}}
	message := "fixture-key-long / fixture-key / custom-token / fixture/token+value / fixture%2Ftoken%2Bvalue\x00\n" + strings.Repeat("错误", 2000)
	failure := &ModelHTTPError{Provider: "model", Code: "context_length_exceeded", Message: message}
	err := modelErrorForDisplay(fmt.Errorf("request failed: %w", failure), model)
	if !IsContextOverflow(err) || !errors.Is(err, failure) || !utf8.ValidString(err.Error()) || len(err.Error()) > maxModelDisplayErrorBytes {
		t.Fatalf("displayed error lost its cause or bound: len=%d, %v", len(err.Error()), err)
	}
	for _, value := range []string{"fixture-key", "custom-token", "fixture/token+value", "fixture%2Ftoken%2Bvalue", "[redacted]-long", "\x00"} {
		if strings.Contains(err.Error(), value) {
			t.Fatalf("displayed error leaked %q", value)
		}
	}
	if !strings.HasSuffix(err.Error(), "…") {
		t.Fatal("truncated explanation is not marked")
	}
	// A key overlapping the display boundary must be removed in full first.
	model.APIKey = strings.Repeat("credential", 100)
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"message": strings.Repeat("x", 8000) + model.APIKey + strings.Repeat("y", 200)}})
	upstream := modelStreamError("model", body)
	if display := modelErrorForDisplay(upstream, model); strings.Contains(display.Error(), "credential") || !strings.Contains(display.Error(), "[redacted]") {
		t.Fatal("truncation left a credential prefix")
	}
}

func TestModelHTTPErrorBodyReadHasTotalDeadline(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	response := &http.Response{StatusCode: 503, Header: http.Header{}, Body: reader}
	started := time.Now()
	var failure *ModelHTTPError
	if err := modelHTTPError("model", response); !errors.As(err, &failure) || failure.StatusCode != 503 {
		t.Fatalf("stalled error body hid HTTP status: %+v, %v", failure, err)
	}
	if elapsed := time.Since(started); elapsed < 2*time.Second || elapsed > 4*time.Second {
		t.Fatalf("stalled error body took %s", elapsed)
	}
}
