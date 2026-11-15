package provider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	modelcfg "vista/internal/model"
	"vista/internal/modeltrace"
	"vista/internal/network"
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
		{"connection", &url.Error{Op: "Post", URL: "http://fixture", Err: &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}}, true},
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

func TestModelRetryTransportCausesSurviveProxySanitizing(t *testing.T) {
	certificate := &x509.Certificate{DNSNames: []string{"valid.test"}}
	hostname := x509.HostnameError{Certificate: certificate, Host: "wrong.test"}
	policy := network.Policy{Profile: &network.Profile{ID: "proxy", Name: "Proxy", Protocol: "http", Server: "entry.test", Port: 8080, Authentication: "none"}}
	for _, check := range []struct {
		name string
		err  error
		want bool
	}{
		{"hostname", hostname, false},
		{"untrusted", x509.UnknownAuthorityError{Cert: certificate}, false},
		{"expired", x509.CertificateInvalidError{Cert: certificate, Reason: x509.Expired}, false},
		{"system roots", x509.SystemRootsError{Err: errors.New("roots unavailable")}, false},
		{"TLS certificate wrapper", &tls.CertificateVerificationError{Err: hostname}, false},
		{"certificate inside connection", &net.OpError{Op: "dial", Net: "tcp", Err: hostname}, false},
		{"wrong TLS endpoint", tls.RecordHeaderError{Msg: "HTTP response to HTTPS client"}, false},
		{"opaque permanent", errors.New("endpoint redirected"), false},
		{"invalid address", &net.AddrError{Err: "invalid address", Addr: "invalid"}, false},
		{"permanent DNS", &net.DNSError{Err: "no such host", IsNotFound: true}, false},
		{"opaque connection failure", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("permanent handshake failure")}, false},
		{"connection with invalid address", &net.OpError{Op: "dial", Net: "tcp", Err: &net.AddrError{Err: "invalid address", Addr: "invalid"}}, false},
		{"connection with permanent DNS", &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", IsNotFound: true}}, false},
		{"connection with wrong TLS endpoint", &net.OpError{Op: "dial", Net: "tcp", Err: tls.RecordHeaderError{Msg: "HTTP response to HTTPS client"}}, false},
		{"connection permission denied", &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.EACCES}}, false},
		{"timeout", context.DeadlineExceeded, true},
		{"temporary DNS", &net.DNSError{Err: "temporary failure", IsTemporary: true}, true},
		{"connection refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, true},
		{"connection reset", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, true},
		{"connection aborted", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNABORTED}, true},
		{"broken pipe", &net.OpError{Op: "write", Net: "tcp", Err: syscall.EPIPE}, true},
		{"closed connection", &net.OpError{Op: "read", Net: "tcp", Err: net.ErrClosed}, true},
		{"pre-header EOF", io.EOF, true},
		{"pre-header unexpected EOF", io.ErrUnexpectedEOF, true},
		{"cancelled", context.Canceled, false},
	} {
		for _, proxied := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/proxy=%v", check.name, proxied), func(t *testing.T) {
				cause := fmt.Errorf("request failed: %w", check.err)
				if proxied {
					cause = network.Sanitize(cause, policy)
				}
				err := modelErrorForDisplay(&url.Error{Op: "Post", URL: "https://model.test", Err: cause}, modelcfg.Model{})
				if _, got := modelRetryDelay(context.Background(), err, 1); got != check.want {
					t.Fatalf("retry=%v want=%v: %v", got, check.want, err)
				}
			})
		}
	}
}

func TestModelRetrySOCKSAuthenticationRejectionIsPermanent(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		for _, rejectMethod := range []bool{false, true} {
			t.Run(fmt.Sprintf("pinned=%v/method-rejection=%v", pinned, rejectMethod), func(t *testing.T) {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				var connections, rejections atomic.Int32
				done := make(chan struct{})
				go func() {
					defer close(done)
					for {
						conn, err := listener.Accept()
						if err != nil {
							return
						}
						connections.Add(1)
						func() {
							defer conn.Close()
							conn.SetDeadline(time.Now().Add(2 * time.Second))
							var header [2]byte
							if _, err := io.ReadFull(conn, header[:]); err != nil || header[0] != 5 {
								return
							}
							if _, err := io.CopyN(io.Discard, conn, int64(header[1])); err != nil {
								return
							}
							if rejectMethod {
								rejections.Add(1)
								conn.Write([]byte{5, 0xff})
								return
							}
							if _, err := conn.Write([]byte{5, 2}); err != nil {
								return
							}
							if _, err := io.ReadFull(conn, header[:]); err != nil || header[0] != 1 {
								return
							}
							if _, err := io.CopyN(io.Discard, conn, int64(header[1])); err != nil {
								return
							}
							if _, err := io.ReadFull(conn, header[:1]); err != nil {
								return
							}
							if _, err := io.CopyN(io.Discard, conn, int64(header[0])); err != nil {
								return
							}
							rejections.Add(1)
							conn.Write([]byte{1, 1})
						}()
					}
				}()
				t.Cleanup(func() {
					listener.Close()
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("SOCKS fixture did not close")
					}
				})
				profile := network.Profile{ID: "proxy", Name: "Proxy", Protocol: "socks5", Server: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, Authentication: "password", Username: "fixture-user", Password: "fixture-password"}
				policy := network.Policy{Profile: &profile}
				ctx, cancel := context.WithTimeout(fastModelRetryContext(), 3*time.Second)
				defer cancel()
				if pinned {
					ctx, err = network.WithAddresses(ctx, "model.fixture.invalid", []net.IP{net.ParseIP("192.0.2.1")})
					if err != nil {
						t.Fatal(err)
					}
				}
				var states []ModelRetry
				ctx = WithModelRetryObserver(ctx, func(state ModelRetry) error { states = append(states, state); return nil })
				model := modelcfg.Model{Provider: "openai-chat", ID: "fixture", BaseURL: "https://model.fixture.invalid", APIKey: "fixture-key", Network: &policy}
				_, err = ModelTurn(ctx, model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
				var requestError *url.Error
				if !errors.As(err, &requestError) || connections.Load() != 1 || rejections.Load() != 1 || len(states) != 0 || ctx.Err() != nil {
					t.Fatalf("SOCKS refusal retried: connections=%d rejections=%d states=%d err=%v context=%v", connections.Load(), rejections.Load(), len(states), err, ctx.Err())
				}
			})
		}
	}
}

func TestModelRetryCertificateFailuresStopBeforeAnotherConnection(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		for _, proxied := range []bool{false, true} {
			t.Run(fmt.Sprintf("trusted=%v/proxy=%v", trusted, proxied), func(t *testing.T) {
				var connections, requests, tunnels atomic.Int32
				endpoint := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					http.Error(w, "certificate failure reached HTTP", http.StatusInternalServerError)
				}))
				endpoint.Config.ErrorLog = log.New(io.Discard, "", 0)
				endpoint.Config.ConnState = func(_ net.Conn, state http.ConnState) {
					if state == http.StateNew {
						connections.Add(1)
					}
				}
				endpoint.StartTLS()
				defer endpoint.Close()
				proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					tunnels.Add(1)
					if r.Method != http.MethodConnect {
						t.Error("expected CONNECT")
						http.Error(w, "expected CONNECT", http.StatusBadRequest)
						return
					}
					upstream, err := net.DialTimeout("tcp", endpoint.Listener.Addr().String(), time.Second)
					if err != nil {
						t.Error(err)
						return
					}
					downstream, buffer, err := w.(http.Hijacker).Hijack()
					if err != nil {
						upstream.Close()
						t.Error(err)
						return
					}
					defer downstream.Close()
					defer upstream.Close()
					buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
					buffer.Flush()
					go func() { io.Copy(upstream, buffer); upstream.Close() }()
					io.Copy(downstream, upstream)
				}))
				defer proxy.Close()
				roots := x509.NewCertPool()
				serverName := "127.0.0.1"
				if trusted {
					roots.AddCert(endpoint.Certificate())
					serverName = "wrong.test"
				}
				base := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: serverName}}
				previous := client
				client = network.NewClient(http.Client{Transport: base, Timeout: 2 * time.Second}, network.ConnectionScope)
				defer func() { client.CloseIdleConnections(); client = previous }()
				policy := network.Policy{}
				baseURL := endpoint.URL
				if proxied {
					u, _ := url.Parse(proxy.URL)
					port, _ := strconv.Atoi(u.Port())
					policy.Profile = &network.Profile{ID: "proxy", Name: "Proxy", Protocol: "http", Server: u.Hostname(), Port: port, Authentication: "none"}
					// Loopback destinations intentionally bypass proxies.
					baseURL = "https://model.fixture.invalid"
				}
				var states []ModelRetry
				ctx := WithModelRetryObserver(fastModelRetryContext(), func(state ModelRetry) error { states = append(states, state); return nil })
				model := modelcfg.Model{Provider: "openai-chat", ID: "fixture", BaseURL: baseURL, APIKey: "fixture", Network: &policy}
				_, err := ModelTurn(ctx, model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
				var verification *tls.CertificateVerificationError
				if !errors.As(err, &verification) || connections.Load() != 1 || requests.Load() != 0 || len(states) != 0 || proxied && tunnels.Load() != 1 {
					t.Fatalf("permanent TLS error retried: connections=%d requests=%d tunnels=%d states=%d err=%v", connections.Load(), requests.Load(), tunnels.Load(), len(states), err)
				}
			})
		}
	}
}

func TestModelRetryProxiedTransientFailuresStillRecover(t *testing.T) {
	for _, failure := range []string{"close", "reset", "header-timeout"} {
		t.Run(failure, func(t *testing.T) {
			var attempts atomic.Int32
			stop := make(chan struct{})
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				if attempts.Add(1) == 1 {
					if failure == "header-timeout" {
						select {
						case <-r.Context().Done():
						case <-stop:
						}
					} else {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						if failure == "reset" {
							if err := conn.(*net.TCPConn).SetLinger(0); err != nil {
								t.Error(err)
							}
						}
						conn.Close()
					}
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"recovered\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer func() { close(stop); proxy.Close() }()
			u, _ := url.Parse(proxy.URL)
			port, _ := strconv.Atoi(u.Port())
			policy := network.Policy{Profile: &network.Profile{ID: "proxy", Name: "Proxy", Protocol: "http", Server: u.Hostname(), Port: port, Authentication: "none"}}
			base := &http.Transport{ResponseHeaderTimeout: 100 * time.Millisecond}
			previous := client
			client = network.NewClient(http.Client{Transport: base, Timeout: 2 * time.Second}, network.ConnectionScope)
			defer func() { client.CloseIdleConnections(); client = previous }()
			ctx, cancel := context.WithTimeout(fastModelRetryContext(), 3*time.Second)
			defer cancel()
			model := modelcfg.Model{Provider: "openai-chat", ID: "fixture", BaseURL: "http://model.fixture.invalid", APIKey: "fixture", Network: &policy}
			turn, err := ModelTurn(ctx, model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if err != nil || turn.Text != "recovered" || attempts.Load() != 2 {
				t.Fatalf("transient recovery lost: attempts=%d text=%q err=%v", attempts.Load(), turn.Text, err)
			}
		})
	}
}

func TestModelRetryNativeConnectionRefusalStillRecovers(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	dialer := &net.Dialer{Timeout: time.Second}
	conn, refused := dialer.DialContext(context.Background(), "tcp", address)
	if conn != nil {
		conn.Close()
	}
	var errno syscall.Errno
	if !errors.As(refused, &errno) {
		t.Fatalf("native connection-refusal fixture failed: %v", refused)
	}
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"recovered\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer endpoint.Close()
	var attempts atomic.Int32
	base := &http.Transport{DialContext: func(ctx context.Context, protocol, address string) (net.Conn, error) {
		if attempts.Add(1) == 1 {
			return nil, refused
		}
		return dialer.DialContext(ctx, protocol, address)
	}}
	previous := client
	client = network.NewClient(http.Client{Transport: base, Timeout: 2 * time.Second}, network.ConnectionScope)
	defer func() { client.CloseIdleConnections(); client = previous }()
	ctx, cancel := context.WithTimeout(fastModelRetryContext(), 3*time.Second)
	defer cancel()
	model := modelcfg.Model{Provider: "openai-chat", ID: "fixture", BaseURL: endpoint.URL, APIKey: "fixture"}
	turn, err := ModelTurn(ctx, model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
	if err != nil || turn.Text != "recovered" || attempts.Load() != 2 {
		t.Fatalf("native connection refusal did not recover: attempts=%d errno=%v text=%q err=%v", attempts.Load(), errno, turn.Text, err)
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
