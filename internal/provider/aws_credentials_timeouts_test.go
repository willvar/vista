package provider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/signin"
	"github.com/aws/aws-sdk-go-v2/service/sso"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go/middleware"

	"vista/internal/network"
)

type timeoutRetryQuota struct{ cost uint }

func (q *timeoutRetryQuota) GetToken(_ context.Context, cost uint) (func() error, error) {
	q.cost = cost
	return func() error { return nil }, nil
}
func (*timeoutRetryQuota) AddTokens(uint) error { return nil }

func TestAWSCredentialProxyHeaderTimeoutClassification(t *testing.T) {
	t.Setenv("AWS_NEW_RETRIES_2026", "false")
	stop := make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	defer func() { close(stop); proxy.Close() }()
	proxyURL, _ := url.Parse(proxy.URL)
	host, portText, _ := net.SplitHostPort(proxyURL.Host)
	port, _ := strconv.Atoi(portText)
	policy := network.Policy{Profile: &network.Profile{ID: "proxy", Name: "Proxy", Protocol: "http", Server: host, Port: port, Authentication: "none"}}
	for _, wrapped := range []bool{false, true} {
		name := "native"
		if wrapped {
			name = "vista"
		}
		t.Run(name, func(t *testing.T) {
			native := awshttp.NewBuildableClient().WithTransportOptions(func(transport *http.Transport) {
				transport.Proxy = http.ProxyURL(proxyURL)
				transport.ResponseHeaderTimeout = 50 * time.Millisecond
			})
			var client interface {
				Do(*http.Request) (*http.Response, error)
			} = native
			ctx, cancel := context.WithTimeout(network.WithConnectionPolicy(context.Background(), policy), 3*time.Second)
			defer cancel()
			if wrapped {
				guarded, err := awsCredentialHTTPClient(ctx, native)
				if err != nil {
					t.Fatal(err)
				}
				defer guarded.CloseIdleConnections()
				client = guarded
			}
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://credentials.test/timeout", nil)
			response, err := client.Do(request)
			if response != nil {
				response.Body.Close()
			}
			var caller *url.Error
			if !errors.As(err, &caller) || !caller.Timeout() || !caller.Temporary() || ctx.Err() != nil || !(retry.TimeouterError{}).IsErrorTimeout(err).Bool() {
				t.Fatalf("HTTP/SDK timeout semantics lost: %v, operation=%v", err, ctx.Err())
			}
			quota := &timeoutRetryQuota{}
			retryer := retry.NewStandard(func(options *retry.StandardOptions) { options.RateLimiter = quota })
			if _, err := retryer.GetRetryToken(ctx, err); err != nil || quota.cost != 10 {
				t.Fatalf("timeout retry cost=%d, want 10; error=%v", quota.cost, err)
			}
		})
	}
}

func TestAWSCredentialServiceTimeouts(t *testing.T) {
	ctx := network.WithConnectionPolicy(context.Background(), network.Policy{})
	var connectBudget atomic.Int64
	oldTransport := http.DefaultTransport
	http.DefaultTransport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("SDK connect budget missing")
		}
		connectBudget.Store(int64(time.Until(deadline)))
		return nil, errors.New("fixture stops before connecting")
	}}
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	services := []string{sts.ServiceID, sso.ServiceID, ssooidc.ServiceID, signin.ServiceID, imds.ServiceID, awsContainerService}
	for _, mode := range []struct {
		name        string
		mode        aws.DefaultsMode
		environment aws.RuntimeEnvironment
		connect     time.Duration
		tls         time.Duration
	}{
		{"legacy", aws.DefaultsModeLegacy, aws.RuntimeEnvironment{}, 30 * time.Second, 10 * time.Second},
		{"standard", aws.DefaultsModeStandard, aws.RuntimeEnvironment{}, 3100 * time.Millisecond, 3100 * time.Millisecond},
		{"mobile", aws.DefaultsModeMobile, aws.RuntimeEnvironment{}, 30 * time.Second, 30 * time.Second},
		{"in-region", aws.DefaultsModeInRegion, aws.RuntimeEnvironment{}, 1100 * time.Millisecond, 1100 * time.Millisecond},
		{"cross-region", aws.DefaultsModeCrossRegion, aws.RuntimeEnvironment{}, 3100 * time.Millisecond, 3100 * time.Millisecond},
		{"auto-unknown", aws.DefaultsModeAuto, aws.RuntimeEnvironment{}, 3100 * time.Millisecond, 3100 * time.Millisecond},
		{"auto-in-region", aws.DefaultsModeAuto, aws.RuntimeEnvironment{EC2InstanceMetadataRegion: "us-east-1"}, 1100 * time.Millisecond, 1100 * time.Millisecond},
		{"auto-cross-region", aws.DefaultsModeAuto, aws.RuntimeEnvironment{EC2InstanceMetadataRegion: "eu-west-1"}, 3100 * time.Millisecond, 3100 * time.Millisecond},
	} {
		for _, service := range services {
			t.Run(mode.name+"/"+service, func(t *testing.T) {
				cfg := aws.Config{Region: "us-east-1", DefaultsMode: mode.mode, RuntimeEnvironment: mode.environment,
					HTTPClient: awshttp.NewBuildableClient().WithTimeout(2 * time.Minute).WithReadTimeout(0)}
				dispatch := &awsCredentialNetwork{operation: ctx, config: cfg, clients: make(map[string]*network.RuntimeIdentityClient)}
				defer dispatch.CloseIdleConnections()
				requestCtx := middleware.WithServiceID(ctx, service)
				requestCtx = awsmiddleware.SetRegion(requestCtx, "us-east-1")
				if service == imds.ServiceID || service == awsContainerService {
					requestCtx = context.WithValue(requestCtx, awsMetadataRequestKey{}, service)
				}
				request, _ := http.NewRequestWithContext(requestCtx, http.MethodGet, "https://127.0.0.1:1/", nil)
				connectBudget.Store(0)
				if _, err := dispatch.Do(request); err == nil {
					t.Fatal("fixture unexpectedly connected")
				}
				wantConnect, wantTLS, wantHeader := mode.connect, mode.tls, time.Duration(0)
				if service == imds.ServiceID {
					wantConnect, wantTLS, wantHeader = 250*time.Millisecond, 10*time.Second, 500*time.Millisecond
				} else if service == awsContainerService {
					wantConnect, wantTLS = 30*time.Second, 10*time.Second
				}
				if got := time.Duration(connectBudget.Load()); got <= wantConnect-100*time.Millisecond || got > wantConnect {
					t.Fatalf("connect budget=%s, want %s", got, wantConnect)
				}
				client, err := dispatch.client(service, "us-east-1")
				if err != nil || client.Timeout != 2*time.Minute {
					t.Fatalf("client timeout lost: %v", err)
				}
				var transport http.RoundTripper = client.Transport
				for {
					if leaf, ok := transport.(network.Transport); ok {
						native := leaf.Base.(*http.Transport)
						if native.TLSHandshakeTimeout != wantTLS || native.ResponseHeaderTimeout != wantHeader {
							t.Fatalf("TLS/header timeout=%s/%s, want %s/%s", native.TLSHandshakeTimeout, native.ResponseHeaderTimeout, wantTLS, wantHeader)
						}
						break
					}
					wrapper, ok := transport.(interface{ Unwrap() http.RoundTripper })
					if !ok {
						t.Fatalf("network leaf missing: %T", transport)
					}
					transport = wrapper.Unwrap()
				}
			})
		}
	}
}

func TestBedrockMobileTLSHandshake(t *testing.T) {
	file := isolateRuntimeCredentials(t)
	t.Setenv("AWS_DEFAULTS_MODE", "mobile")
	t.Setenv("AWS_MAX_ATTEMPTS", "1")
	profile := "[default]\nrole_arn=arn:aws:iam::123456789012:role/fixture\nsource_profile=source\n[profile source]\naws_access_key_id=fixture-id\naws_secret_access_key=fixture-secret\n"
	if err := os.WriteFile(file, []byte(profile), 0600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/xml")
		io.WriteString(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials><AccessKeyId>assumed-id</AccessKeyId><SecretAccessKey>assumed-secret</SecretAccessKey><SessionToken>assumed-session</SessionToken><Expiration>2099-01-01T00:00:00Z</Expiration></Credentials></AssumeRoleResult></AssumeRoleResponse>`)
	}))
	server.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		// This exceeds the old adapter's 10s budget, while remaining well within
		// the SDK mobile mode's 30s budget. No real provider is contacted.
		time.Sleep(10300 * time.Millisecond)
		return nil, nil
	}}
	server.StartTLS()
	defer server.Close()
	t.Setenv("AWS_CA_BUNDLE", writeAWSCA(t, server.Certificate()))
	t.Setenv("AWS_ENDPOINT_URL_STS", server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resolved, err := resolveRuntimeCredentials(ctx, bedrockCredentialModel(network.Policy{}), false)
	if err != nil || resolved.AWSAccessID != "assumed-id" || requests.Load() != 1 {
		t.Fatalf("mobile TLS budget lost: %v requests=%d", err, requests.Load())
	}
}

func TestAWSCredentialReadInactivity(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stalled":
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case "/progress":
			for range 20 {
				io.WriteString(w, "x")
				w.(http.Flusher).Flush()
				time.Sleep(20 * time.Millisecond)
			}
		default:
			io.WriteString(w, "ok")
		}
	}))
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(network.WithConnectionPolicy(context.Background(), network.Policy{}), 3*time.Second)
	defer cancel()
	client, err := awsCredentialHTTPClient(ctx, awshttp.NewBuildableClient().WithReadTimeout(200*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	response, err := client.Get(server.URL + "/progress")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != strings.Repeat("x", 20) {
		t.Fatalf("rolling read timeout became an overall deadline: %v", err)
	}
	response, err = client.Get(server.URL + "/stalled")
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(response.Body)
	response.Body.Close()
	var timeout *awshttp.ResponseTimeoutError
	if !errors.As(err, &timeout) || timeout.TimeoutDur != 200*time.Millisecond {
		t.Fatalf("SDK inactivity timeout or classification lost: %v", err)
	}
}

func TestAWSCredentialServicePools(t *testing.T) {
	var opened atomic.Int32
	closed := make(chan struct{}, 4)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			opened.Add(1)
		} else if state == http.StateClosed {
			closed <- struct{}{}
		}
	}
	server.Start()
	defer server.Close()
	ctx := network.WithConnectionPolicy(context.Background(), network.Policy{})
	dispatch := &awsCredentialNetwork{operation: ctx, config: aws.Config{Region: "us-east-1", HTTPClient: awshttp.NewBuildableClient()}, clients: make(map[string]*network.RuntimeIdentityClient)}
	defer dispatch.CloseIdleConnections()
	for _, service := range []string{sts.ServiceID, sso.ServiceID, ssooidc.ServiceID, signin.ServiceID} {
		for range 2 {
			request, _ := http.NewRequestWithContext(middleware.WithServiceID(ctx, service), http.MethodGet, server.URL, nil)
			response, err := dispatch.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
	}
	if opened.Load() != 4 {
		t.Fatalf("each SDK service should reuse its own pool: connections=%d", opened.Load())
	}
	dispatch.CloseIdleConnections()
	for range 4 {
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("an SDK service idle pool was not closed")
		}
	}
}

func TestAWSNativeDerivedClientCannotBypassVista(t *testing.T) {
	isolateRuntimeCredentials(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture-id")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture-secret")
	t.Setenv("AWS_DEFAULTS_MODE", "mobile")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	ctx := network.WithConnectionPolicy(context.Background(), network.Policy{})
	cfg, release, err := bedrockAWSConfig(ctx, "us-east-1")
	defer release()
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range []string{sts.ServiceID, sso.ServiceID, ssooidc.ServiceID, signin.ServiceID, imds.ServiceID, awsContainerService} {
		t.Run(service, func(t *testing.T) {
			client, err := awsCredentialServiceHTTP(cfg, service)
			if err != nil {
				t.Fatal(err)
			}
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
			response, err := client.Do(request)
			if response != nil {
				response.Body.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "bypassed Vista HTTP dispatcher") || requests.Load() != 0 {
				t.Fatalf("native %s client escaped: %v requests=%d", service, err, requests.Load())
			}
		})
	}
}

func TestAWSHTTPSProxyTLSBudget(t *testing.T) {
	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Error("unexpected non-CONNECT proxy request")
			return
		}
		conn, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		io.WriteString(buffer, "HTTP/1.1 200 Connection Established\r\n\r\n")
		buffer.Flush()
		if _, err := http.ReadRequest(buffer.Reader); err != nil {
			return
		}
		// Serve the synthetic origin inside the tunnel without forwarding a socket.
		io.WriteString(buffer, "HTTP/1.1 200 OK\r\nContent-Length: 1\r\nConnection: close\r\n\r\nx")
		buffer.Flush()
	}))
	proxy.Config.ErrorLog = log.New(io.Discard, "", 0)
	proxy.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		time.Sleep(1500 * time.Millisecond)
		return nil, nil
	}}
	proxy.StartTLS()
	defer proxy.Close()
	roots := x509.NewCertPool()
	roots.AddCert(proxy.Certificate())
	base := awshttp.NewBuildableClient().WithTransportOptions(func(tr *http.Transport) { tr.TLSClientConfig.RootCAs = roots })
	native, err := awsCredentialServiceHTTP(aws.Config{Region: "us-east-1", DefaultsMode: aws.DefaultsModeInRegion, HTTPClient: base}, sts.ServiceID)
	if err != nil {
		t.Fatal(err)
	}
	if native.GetTransport().TLSHandshakeTimeout != 1100*time.Millisecond {
		t.Fatal("fixture expected the installed SDK in-region TLS budget")
	}
	port := proxy.Listener.Addr().(*net.TCPAddr).Port
	for _, route := range []string{"native", "pinned"} {
		t.Run(route, func(t *testing.T) {
			policy := network.Policy{
				Profile: &network.Profile{ID: "fixture-https", Name: "Fixture HTTPS", Protocol: "https", Server: "127.0.0.1", Port: port, Authentication: "none"},
			}
			ctx, cancel := context.WithTimeout(network.WithConnectionPolicy(context.Background(), policy), 5*time.Second)
			defer cancel()
			endpoint := "http://198.51.100.1/fixture"
			if route == "pinned" {
				var err error
				ctx, err = network.WithAddresses(ctx, "credentials.invalid", []net.IP{net.ParseIP("198.51.100.1")})
				if err != nil {
					t.Fatal(err)
				}
				endpoint = "http://credentials.invalid/fixture"
			}
			client, err := awsCredentialHTTPClient(ctx, native)
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			started := time.Now()
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			response, err := client.Do(request)
			if response != nil {
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
			}
			t.Logf("HTTPS proxy handshake elapsed=%s error=%v", time.Since(started), err)
			if err == nil || !strings.Contains(err.Error(), "TLS handshake timeout") {
				t.Fatalf("SDK TLS handshake budget lost for HTTPS proxy on %s: %v", route, err)
			}
		})
	}
}
