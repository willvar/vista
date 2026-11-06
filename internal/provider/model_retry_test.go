package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	modelcfg "vista/internal/model"
	"vista/internal/modeltrace"
)

func fastModelRetryContext() context.Context {
	return context.WithValue(context.Background(), modelRetryDelayKey{}, time.Microsecond)
}

func TestModelRetryRecoversOneRequestWithIndependentDiagnosticsAndUsage(t *testing.T) {
	dir := configureTestUserAgent(t)
	var bodies []string
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if len(bodies) == 1 {
			w.WriteHeader(502)
			fmt.Fprint(w, `{"error":{"type":"server_error","message":"upstream request error: secret-key"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"recovered\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":5}}\n\ndata: [DONE]\n\n")
	}))
	defer endpoint.Close()
	var states []ModelRetry
	ctx := WithModelRetryObserver(fastModelRetryContext(), func(state ModelRetry) error {
		states = append(states, state)
		if strings.Contains(state.Error, "secret-key") {
			t.Fatal("retry event leaked credentials")
		}
		return nil
	})
	var usage []int64
	ctx = WithUsageRecorder(ctx, func(_ string, value *int64) {
		if value == nil {
			t.Error("failed request counted as missing usage")
			return
		}
		usage = append(usage, *value)
	})
	scope := modeltrace.Scope{Enabled: true, SessionID: uuid.NewString(), RunID: uuid.NewString(), Turn: 2, Purpose: "conversation"}
	ctx = modeltrace.WithScope(ctx, scope)
	model := modelcfg.Model{Provider: "openai-chat", ID: "fixture", BaseURL: endpoint.URL, APIKey: "secret-key"}
	var visible strings.Builder
	turn, err := ModelTurnStream(ctx, model, []ChatMessage{{Role: "user", Content: "preserved input"}}, nil, func(s string) error { visible.WriteString(s); return nil })
	if err != nil || turn.Text != "recovered" || visible.String() != "recovered" || len(bodies) != 2 || bodies[0] != bodies[1] {
		t.Fatalf("request recovery: requests=%d text=%q turn=%+v err=%v", len(bodies), visible.String(), turn, err)
	}
	if len(states) != 3 || states[0].Status != "waiting" || states[0].RetryAt.IsZero() || states[1].Status != "requesting" || states[2].Status != "succeeded" || !reflect.DeepEqual(usage, []int64{105}) {
		t.Fatalf("retry lifecycle or usage: states=%+v usage=%v", states, usage)
	}
	records, err := modeltrace.List(filepath.Join(dir, "state"), scope.SessionID, scope.RunID)
	if err != nil || len(records) != 2 || records[0].Error == "" || records[1].Error != "" || records[0].ID == records[1].ID {
		t.Fatalf("attempt diagnostics: %+v %v", records, err)
	}
}

func TestModelRetryClassificationAndProviderWaits(t *testing.T) {
	for _, check := range []struct {
		name string
		err  error
		want bool
	}{
		{"gateway", &ModelHTTPError{StatusCode: 502}, true},
		{"rate limit", &ModelHTTPError{StatusCode: 429, Code: "rate_limit_exceeded"}, true},
		{"quota", &ModelHTTPError{StatusCode: 429, Code: "insufficient_quota"}, false},
		{"auth", &ModelHTTPError{StatusCode: 401}, false},
		{"bad request", &ModelHTTPError{StatusCode: 400}, false},
		{"unsupported endpoint", &ModelHTTPError{StatusCode: 429, Code: "unsupported_endpoint"}, false},
		{"overflow", &ModelHTTPError{StatusCode: 503, Code: "context_length_exceeded"}, false},
		{"stream error", &ModelHTTPError{Code: "server_error"}, false},
		{"cancel", context.Canceled, false},
		{"protocol", errors.New("invalid SSE"), false},
		{"connection", &url.Error{Op: "Post", URL: "http://fixture", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}}, true},
		{"pre-header EOF", &url.Error{Op: "Post", URL: "http://fixture", Err: io.EOF}, true},
		{"redirect", &url.Error{Op: "Post", URL: "http://fixture", Err: errors.New("model endpoint redirected")}, false},
	} {
		t.Run(check.name, func(t *testing.T) {
			if _, got := modelRetryDelay(context.Background(), check.err, 1); got != check.want {
				t.Fatalf("classification=%v want=%v: %v", got, check.want, check.err)
			}
		})
	}
	if wait, ok := modelRetryDelay(context.Background(), &ModelHTTPError{StatusCode: 429, RetryAfter: 30 * time.Second}, 1); !ok || wait != 30*time.Second {
		t.Fatalf("server wait shortened: %s %v", wait, ok)
	}
	if _, ok := modelRetryDelay(context.Background(), &ModelHTTPError{StatusCode: 429, RetryAfter: 2 * time.Minute}, 1); ok {
		t.Fatal("excessive wait was silently shortened or accepted")
	}
	for attempt := 1; attempt <= MaxModelRetries; attempt++ {
		want := 2 * time.Second * time.Duration(1<<(attempt-1))
		if delay, ok := modelRetryDelay(context.Background(), &ModelHTTPError{StatusCode: 503}, attempt); !ok || delay < want*8/10 || delay > want*12/10 {
			t.Fatalf("retry %d backoff: %s, eligible=%v", attempt, delay, ok)
		}
	}
}

func TestModelRetryAfterHeaders(t *testing.T) {
	for _, check := range []struct {
		name, milliseconds, seconds string
		want                        time.Duration
	}{
		{"milliseconds", "3500", "10", 3500 * time.Millisecond},
		{"seconds", "", "30", 30 * time.Second},
		{"invalid milliseconds", "invalid", "15", 15 * time.Second},
		{"negative", "-10", "-1", 0},
		{"long wait", "", "172800", 48 * time.Hour},
	} {
		t.Run(check.name, func(t *testing.T) {
			headers := http.Header{"Retry-After-Ms": {check.milliseconds}, "Retry-After": {check.seconds}}
			if got := modelRetryAfter(headers); got != check.want {
				t.Fatalf("wait=%s want=%s", got, check.want)
			}
		})
	}
	for _, headers := range []http.Header{
		{"Retry-After": {time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat)}},
		{"Retry-After": {"9223372036854775807"}},
		{"Retry-After-Ms": {"9223372036854775807"}},
	} {
		if _, ok := modelRetryDelay(context.Background(), &ModelHTTPError{StatusCode: 429, RetryAfter: modelRetryAfter(headers)}, 1); ok {
			t.Fatalf("excessive wait accepted: %v", headers)
		}
	}
}

func TestModelRetryOwnsBedrockAttemptBudget(t *testing.T) {
	configureTestUserAgent(t)
	requests := 0
	endpoint := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Amzn-Errortype", "ServiceUnavailableException")
		w.WriteHeader(503)
		fmt.Fprint(w, `{"message":"Service temporarily unavailable"}`)
	}))
	defer endpoint.Close()
	previous := bedrockHTTPClient.Transport
	bedrockHTTPClient.Transport = bedrockTransport{base: endpoint.Client().Transport}
	defer func() { bedrockHTTPClient.Transport = previous }()
	model := modelcfg.Model{Provider: "bedrock", ID: "amazon.nova-micro-v1:0", AWSRegion: "us-east-1", APIKey: "private-token", BaseURL: endpoint.URL}
	_, err := ModelTurn(fastModelRetryContext(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
	var failure *ModelHTTPError
	if requests != MaxModelRetries+1 || !errors.As(err, &failure) || failure.StatusCode != 503 || failure.Code != "ServiceUnavailableException" {
		t.Fatalf("SDK multiplied retries or lost status: requests=%d err=%v", requests, err)
	}
}

func TestModelRetryCancellationAndObserverFailurePreventAnotherRequest(t *testing.T) {
	for _, failObserver := range []bool{false, true} {
		t.Run(fmt.Sprint(failObserver), func(t *testing.T) {
			requests := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests++; w.WriteHeader(502) }))
			defer endpoint.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			persistenceError := errors.New("event store unavailable")
			ctx = WithModelRetryObserver(ctx, func(state ModelRetry) error {
				if failObserver {
					return persistenceError
				}
				cancel()
				return nil
			})
			_, err := ModelTurn(ctx, modelcfg.Model{Provider: "openai-chat", ID: "fixture", BaseURL: endpoint.URL, APIKey: "key"}, []ChatMessage{{Role: "user", Content: "hi"}}, nil)
			want := context.Canceled
			if failObserver {
				want = persistenceError
			}
			if !errors.Is(err, want) || requests != 1 {
				t.Fatalf("requests=%d err=%v", requests, err)
			}
		})
	}
}

func TestModelRetryDoesNotReplayStreamedOrExecutedOutput(t *testing.T) {
	for _, kind := range []string{"text", "reasoning", "tool", "hosted", "usage"} {
		t.Run(kind, func(t *testing.T) {
			requests := 0
			adapter := protocolAdapter{turn: func(_ context.Context, _ modelcfg.Model, _ []ChatMessage, _ []map[string]any, text, reasoning func(string) error) (Turn, error) {
				requests++
				turn := Turn{}
				switch kind {
				case "text":
					_ = text("partial answer")
				case "reasoning":
					_ = reasoning("partial thinking")
				case "tool":
					turn.Calls = []ToolCall{{ID: "call"}}
				case "hosted":
					turn.HostedCalls = []HostedCall{{ID: "hosted"}}
				case "usage":
					turn.Usage = &ModelUsage{InputTokens: 1}
				}
				return turn, &ModelHTTPError{StatusCode: 502}
			}}
			_, err := retryModelTurn(fastModelRetryContext(), modelcfg.Model{Provider: "fixture"}, nil, nil, adapter, func(string) error { return nil }, func(string) error { return nil })
			if err == nil || requests != 1 {
				t.Fatalf("unsafe replay: requests=%d err=%v", requests, err)
			}
		})
	}
}

func TestModelRetryAppliesAcrossHTTPProtocols(t *testing.T) {
	for _, protocol := range []string{"openai-chat", "openai-responses", "anthropic", "gemini", "mistral", "cohere", "vercel-gateway"} {
		t.Run(protocol, func(t *testing.T) {
			requests := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				if requests == 1 {
					w.WriteHeader(502)
				} else {
					w.WriteHeader(401)
				}
			}))
			defer endpoint.Close()
			_, err := ModelTurn(fastModelRetryContext(), modelcfg.Model{Provider: protocol, ID: "fixture", BaseURL: endpoint.URL, APIKey: "key"}, []ChatMessage{{Role: "user", Content: "hi"}}, nil)
			var failure *ModelHTTPError
			if !errors.As(err, &failure) || failure.StatusCode != 401 || requests != 2 {
				t.Fatalf("requests=%d err=%v", requests, err)
			}
		})
	}
}
