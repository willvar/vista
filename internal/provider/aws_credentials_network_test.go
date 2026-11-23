package provider

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"vista/internal/network"
)

func writeAWSCA(t *testing.T, certificate *x509.Certificate) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestBedrockCustomCABundleCredentials(t *testing.T) {
	for _, source := range []string{"environment", "shared"} {
		for _, protocol := range []string{"bedrock", "bedrock-mantle-chat", "bedrock-mantle-responses"} {
			t.Run(source+"-"+protocol, func(t *testing.T) {
				configFile := isolateRuntimeCredentials(t)
				f := newRuntimeCredentialFixture(t)
				certificate := httptest.NewTLSServer(http.NotFoundHandler())
				defer certificate.Close()
				ca := writeAWSCA(t, certificate.Certificate())
				if source == "environment" {
					t.Setenv("AWS_CA_BUNDLE", ca)
					if err := os.WriteFile(configFile, []byte("[default]\nca_bundle = missing-lower-priority-ca.pem\n"), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.WriteFile(configFile, []byte("[default]\nca_bundle = "+ca+"\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				t.Setenv("AWS_ACCESS_KEY_ID", "fixture-env-id")
				t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture-env-secret")
				t.Setenv("AWS_DEFAULTS_MODE", "auto")
				// Auto discovery must use the same guard even though environment keys win.
				sourceModel := bedrockCredentialModel(f.policy)
				sourceModel.Provider = protocol
				resolved, err := resolveRuntimeCredentials(context.Background(), sourceModel, false)
				if err != nil || resolved.AWSAccessID != "fixture-env-id" || len(f.observed()) != 0 {
					t.Fatalf("valid CA/environment credentials failed or leaked metadata: %v %v", err, f.observed())
				}
				for _, invalid := range []string{"missing", "malformed"} {
					t.Run(invalid, func(t *testing.T) {
						file := filepath.Join(t.TempDir(), "invalid-ca.pem")
						if invalid == "malformed" {
							if err := os.WriteFile(file, []byte("not a PEM certificate"), 0600); err != nil {
								t.Fatal(err)
							}
						}
						t.Setenv("AWS_CA_BUNDLE", file)
						if _, err := resolveRuntimeCredentials(context.Background(), sourceModel, false); err == nil || len(f.observed()) != 0 {
							t.Fatalf("invalid CA was ignored: %v %v", err, f.observed())
						}
					})
				}
			})
		}
	}
}

func TestAWSCACredentialTLSAndProxy(t *testing.T) {
	for _, source := range []string{"assume-role", "sso", "container", "ec2"} {
		for _, route := range []string{"direct", "proxy"} {
			t.Run(source+"-"+route, func(t *testing.T) {
				configFile := isolateRuntimeCredentials(t)
				var requests, proxyCalls, directCalls atomic.Int32
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					number := requests.Add(1)
					switch {
					case r.URL.Path == "/sts":
						if !strings.Contains(r.Header.Get("Authorization"), "fixture-env-id") {
							t.Error("SDK signing/source credential precedence lost")
						}
						w.Header().Set("Content-Type", "text/xml")
						if number == 1 {
							w.WriteHeader(500)
							io.WriteString(w, `<ErrorResponse><Error><Code>InternalFailure</Code></Error></ErrorResponse>`)
							return
						}
						io.WriteString(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials><AccessKeyId>assumed-id</AccessKeyId><SecretAccessKey>assumed-secret</SecretAccessKey><SessionToken>assumed-session</SessionToken><Expiration>2099-01-01T00:00:00Z</Expiration></Credentials></AssumeRoleResult></AssumeRoleResponse>`)
					case r.URL.Path == "/token":
						w.Header().Set("Content-Type", "application/json")
						io.WriteString(w, `{"accessToken":"refreshed-sso-token","expiresIn":3600,"refreshToken":"refreshed-refresh-token","tokenType":"Bearer"}`)
					case r.URL.Path == "/federation/credentials":
						if r.Header.Get("x-amz-sso_bearer_token") != "refreshed-sso-token" {
							t.Error("SDK SSO token refresh not used")
						}
						w.Header().Set("Content-Type", "application/json")
						io.WriteString(w, `{"roleCredentials":{"accessKeyId":"sso-id","secretAccessKey":"sso-secret","sessionToken":"sso-session","expiration":4070908800000}}`)
					case r.URL.Path == "/container" || strings.HasSuffix(r.URL.Path, "/fixture-role"):
						if source == "container" && r.Header.Get("Authorization") != "fixture-container-auth" {
							t.Error("container token lost")
						}
						w.Header().Set("Content-Type", "application/json")
						io.WriteString(w, `{"Code":"Success","AccessKeyId":"runtime-id","SecretAccessKey":"runtime-secret","Token":"runtime-session","Expiration":"2099-01-01T00:00:00Z"}`)
					case r.URL.Path == "/latest/api/token":
						w.Header().Set("X-Aws-Ec2-Metadata-Token-Ttl-Seconds", "21600")
						io.WriteString(w, "fixture-imds-token")
					case r.URL.Path == "/latest/meta-data/iam/security-credentials/":
						io.WriteString(w, "fixture-role")
					default:
						t.Errorf("unexpected SDK request: %s", r.URL)
						http.Error(w, "unexpected", 400)
					}
				}))
				server.Config.ErrorLog = log.New(io.Discard, "", 0)
				server.StartTLS()
				defer server.Close()
				ca := writeAWSCA(t, server.Certificate())
				t.Setenv("AWS_CA_BUNDLE", ca)
				_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
				target := "https://example.com:" + port
				proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					proxyCalls.Add(1)
					if r.Method != http.MethodConnect || r.Host != "example.com:"+port {
						t.Errorf("unexpected proxy target: %s %s", r.Method, r.Host)
						http.Error(w, "unexpected", 400)
						return
					}
					upstream, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
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
				base := &http.Transport{DialContext: func(ctx context.Context, kind, address string) (net.Conn, error) {
					switch address {
					case "example.com:" + port:
						directCalls.Add(1)
						address = server.Listener.Addr().String()
					case proxy.Listener.Addr().String():
					default:
						return nil, fmt.Errorf("non-fixture egress blocked: %s", address)
					}
					return new(net.Dialer).DialContext(ctx, kind, address)
				}}
				previous := http.DefaultTransport
				http.DefaultTransport = base
				defer func() { (network.Transport{Base: base}).CloseIdleConnections(); http.DefaultTransport = previous }()
				policy := network.Policy{}
				if route == "proxy" {
					_, proxyPort, _ := net.SplitHostPort(proxy.Listener.Addr().String())
					p, _ := strconv.Atoi(proxyPort)
					policy.Profile = &network.Profile{ID: "selected", Name: "Selected", Protocol: "http", Server: "127.0.0.1", Port: p, Authentication: "none"}
				}
				// A process proxy must not replace the connection's selected route.
				t.Setenv("HTTP_PROXY", "http://forbidden-env-proxy.invalid:9999")
				t.Setenv("HTTPS_PROXY", "http://forbidden-env-proxy.invalid:9999")
				want := "runtime-id"
				switch source {
				case "assume-role":
					text := "[default]\nrole_arn = arn:aws:iam::123456789012:role/fixture\nsource_profile = source\n[profile source]\naws_access_key_id = fixture-env-id\naws_secret_access_key = fixture-env-secret\n"
					if err := os.WriteFile(configFile, []byte(text), 0600); err != nil {
						t.Fatal(err)
					}
					t.Setenv("AWS_ENDPOINT_URL_STS", target+"/sts")
					t.Setenv("AWS_MAX_ATTEMPTS", "2")
					want = "assumed-id"
				case "sso":
					text := "[default]\nsso_session = fixture\nsso_account_id = 123456789012\nsso_role_name = fixture\n[sso-session fixture]\nsso_region = us-east-1\nsso_start_url = https://fixture.awsapps.com/start\n"
					if err := os.WriteFile(configFile, []byte(text), 0600); err != nil {
						t.Fatal(err)
					}
					file, err := ssocreds.StandardCachedTokenFilepath("fixture")
					if err != nil {
						t.Fatal(err)
					}
					if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
						t.Fatal(err)
					}
					data, _ := json.Marshal(map[string]any{"accessToken": "expired", "expiresAt": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "refreshToken": "fixture-refresh", "clientId": "fixture-client", "clientSecret": "fixture-secret"})
					if err := os.WriteFile(file, data, 0600); err != nil {
						t.Fatal(err)
					}
					t.Setenv("AWS_ENDPOINT_URL_SSO", target)
					t.Setenv("AWS_ENDPOINT_URL_SSO_OIDC", target)
					want = "sso-id"
				case "container":
					t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", target+"/container")
					t.Setenv("AWS_CONTAINER_AUTHORIZATION_TOKEN", "fixture-container-auth")
				case "ec2":
					t.Setenv("AWS_EC2_METADATA_SERVICE_ENDPOINT", target)
				}
				ctx, cancel := context.WithTimeout(network.WithConnectionPolicy(context.Background(), policy), 8*time.Second)
				defer cancel()
				if route == "proxy" && (source == "container" || source == "ec2") {
					if _, err := resolveRuntimeCredentials(ctx, bedrockCredentialModel(policy), false); !errors.Is(err, network.ErrRuntimeIdentityProxy) || proxyCalls.Load() != 0 {
						t.Fatalf("CA setup bypassed metadata guard: %v proxy=%d", err, proxyCalls.Load())
					}
					policy.Profile = nil
					ctx = network.WithConnectionPolicy(ctx, policy)
				}
				cfg, release, err := bedrockAWSConfig(ctx, "us-east-1")
				defer release()
				if err != nil {
					t.Fatal(err)
				}
				credentials, err := cfg.Credentials.Retrieve(ctx)
				if err != nil || credentials.AccessKeyID != want {
					t.Fatalf("TLS %s %s failed: %v requests=%d", source, route, err, requests.Load())
				}
				before := requests.Load()
				again, err := cfg.Credentials.Retrieve(ctx)
				if err != nil || again.AccessKeyID != want || requests.Load() != before {
					t.Fatalf("SDK credential cache lost: %v", err)
				}
				if source == "assume-role" && before != 2 {
					t.Fatalf("SDK retry lost: requests=%d", before)
				}
				if source == "sso" && before != 2 {
					t.Fatalf("SSO refresh/role count=%d", before)
				}
				connections := int32(1)
				if source == "sso" {
					// The SDK's token and role clients are distinct services, each
					// with its own resolved HTTP settings and connection pool.
					connections = 2
				}
				if route == "proxy" && (source == "sso" || source == "assume-role") {
					if proxyCalls.Load() != connections || directCalls.Load() != 0 {
						t.Fatalf("selected proxy not used: proxy=%d direct=%d", proxyCalls.Load(), directCalls.Load())
					}
				} else if proxyCalls.Load() != 0 || directCalls.Load() != connections {
					t.Fatalf("direct route or connection reuse lost: proxy=%d direct=%d", proxyCalls.Load(), directCalls.Load())
				}

				// Run the untrusted TLS control last: net/http may finish a dial
				// after request cancellation, outside the connection assertions above.
				t.Setenv("AWS_CA_BUNDLE", "")
				control, stopControl := context.WithTimeout(ctx, 300*time.Millisecond)
				_, controlErr := resolveRuntimeCredentials(control, bedrockCredentialModel(policy), false)
				stopControl()
				if controlErr == nil {
					t.Fatal("untrusted TLS endpoint accepted")
				}
				if requests.Load() != before {
					t.Fatal("TLS control reached credential handler")
				}
			})
		}
	}
}

func TestAWSDetachedCredentialRequestCancellation(t *testing.T) {
	isolateRuntimeCredentials(t)
	started := make(chan struct{})
	stopped := make(chan struct{})
	release := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		close(started)
		select {
		case <-r.Context().Done():
			close(stopped)
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release); server.Close() })
	t.Setenv("AWS_CA_BUNDLE", writeAWSCA(t, server.Certificate()))
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", server.URL+"/wait")
	ctx, cancel := context.WithCancel(network.WithConnectionPolicy(context.Background(), network.Policy{}))
	defer cancel()
	cfg, cleanup, err := bedrockAWSConfig(ctx, "us-east-1")
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := cfg.Credentials.Retrieve(context.Background()); done <- err }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("request did not start: %v", err)
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SDK retried a detached cancelled operation")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("TLS request remained active")
	}
	if requests.Load() != 1 {
		t.Fatalf("requests after cancellation=%d", requests.Load())
	}
}

func TestBedrockNativeClientCannotBypassVista(t *testing.T) {
	isolateRuntimeCredentials(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture-id")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture-secret")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	ctx := network.WithConnectionPolicy(context.Background(), network.Policy{})
	cfg, release, err := bedrockAWSConfig(ctx, "us-east-1")
	defer release()
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	response, err := cfg.HTTPClient.Do(request)
	if response != nil {
		response.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "bypassed Vista HTTP dispatcher") || requests.Load() != 0 {
		t.Fatalf("unadapted native dispatch escaped: %v requests=%d", err, requests.Load())
	}
}
