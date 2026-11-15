package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/network"
)

func isolateRuntimeCredentials(t *testing.T) string {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "AWS_") {
			t.Setenv(name, "")
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("APPDATA", home)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("GCE_METADATA_HOST", "")
	file := filepath.Join(home, "aws-config")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", file)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", file)
	return file
}

type runtimeIdentityRedirect struct{ path, location string }
type runtimeCredentialFixture struct {
	policy                     network.Policy
	origin                     *httptest.Server
	mu                         sync.Mutex
	requests                   []string
	dials                      int
	redirect                   atomic.Pointer[runtimeIdentityRedirect]
	waitPath                   atomic.Pointer[string]
	started, canceled, release chan struct{}
}

func newRuntimeCredentialFixture(t *testing.T) *runtimeCredentialFixture {
	t.Helper()
	f := &runtimeCredentialFixture{started: make(chan struct{}, 1), canceled: make(chan struct{}, 1), release: make(chan struct{})}
	handler := func(route string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			f.requests = append(f.requests, route+" "+r.Host+r.URL.Path)
			f.mu.Unlock()
			if path := f.waitPath.Load(); path != nil && r.URL.Path == *path {
				f.started <- struct{}{}
				select {
				case <-r.Context().Done():
					f.canceled <- struct{}{}
					return
				case <-f.release:
					return
				}
			}
			if redirect := f.redirect.Load(); redirect != nil && r.URL.Path == redirect.path {
				w.Header().Set("Location", redirect.location)
				w.WriteHeader(http.StatusFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/latest/api/token", "/token":
				io.WriteString(w, "fixture-imds-token")
			case "/latest/dynamic/instance-identity/document":
				io.WriteString(w, `{"region":"us-east-1"}`)
			case "/latest/meta-data/iam/security-credentials/", "/roles":
				io.WriteString(w, "fixture-role")
			case "/region":
				io.WriteString(w, "us-east-1a")
			case "/latest/meta-data/iam/security-credentials/fixture-role", "/roles/fixture-role", "/container":
				if r.URL.Path == "/container" && r.Header.Get("Authorization") != "fixture-container-auth" {
					t.Error("SDK container authorization token lost")
				}
				fmt.Fprintf(w, `{"Code":"Success","AccessKeyId":"%s-runtime-id","SecretAccessKey":"fixture-secret","Token":"fixture-session","Expiration":"2099-01-01T00:00:00Z"}`, route)
			case "/google-sts":
				if err := r.ParseForm(); err != nil || r.Form.Get("subject_token") == "" {
					t.Error("SDK federation subject token missing")
				}
				io.WriteString(w, `{"access_token":"fixture-google-token","expires_in":3600,"token_type":"Bearer"}`)
			case "/subject-token":
				io.WriteString(w, "fixture-subject-token")
			case "/impersonate":
				io.WriteString(w, `{"accessToken":"fixture-impersonated-token","expireTime":"2099-01-01T00:00:00Z"}`)
			case "/aws-sts":
				if !strings.Contains(r.Header.Get("Authorization"), "direct-runtime-id") {
					t.Error("AssumeRole did not use this runtime's source credentials")
				}
				w.Header().Set("Content-Type", "text/xml")
				io.WriteString(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials><AccessKeyId>assumed-id</AccessKeyId><SecretAccessKey>assumed-secret</SecretAccessKey><SessionToken>assumed-session</SessionToken><Expiration>2099-01-01T00:00:00Z</Expiration></Credentials></AssumeRoleResult></AssumeRoleResponse>`)
			case "/redirected-identity":
				io.WriteString(w, `{"AccessKeyId":"redirect-id","SecretAccessKey":"redirect-secret","access_token":"redirect-google-token","expires_in":3600,"token_type":"Bearer"}`)
			default:
				t.Errorf("unexpected credential request: %s %s", route, r.URL)
				http.Error(w, "unexpected request", http.StatusBadRequest)
			}
		})
	}
	f.origin = httptest.NewServer(handler("direct"))
	proxy := httptest.NewServer(handler("proxy"))
	entry, _ := url.Parse(proxy.URL)
	port, _ := strconv.Atoi(entry.Port())
	f.policy = network.Policy{Profile: &network.Profile{ID: "selected", Name: "Selected", Protocol: "http", Server: entry.Hostname(), Port: port, Authentication: "none"}}
	base := &http.Transport{DialContext: func(ctx context.Context, kind, address string) (net.Conn, error) {
		f.mu.Lock()
		f.dials++
		f.mu.Unlock()
		switch address {
		case proxy.Listener.Addr().String(), f.origin.Listener.Addr().String():
		case "metadata.fixture.invalid:80", "metadata.google.internal:80", "169.254.169.254:80", "169.254.170.2:80", "169.254.170.23:80", "[fd00:ec2::254]:80", "[fd00:ec2::23]:80", "sts.fixture.invalid:80", "oidc.fixture.invalid:80":
			address = f.origin.Listener.Addr().String()
		default:
			return nil, errors.New("non-fixture socket blocked: " + address)
		}
		return new(net.Dialer).DialContext(ctx, kind, address)
	}}
	previous := http.DefaultTransport
	http.DefaultTransport = base
	t.Cleanup(func() {
		close(f.release)
		(network.Transport{Base: base}).CloseIdleConnections()
		http.DefaultTransport = previous
		f.origin.Close()
		proxy.Close()
	})
	return f
}
func (f *runtimeCredentialFixture) observed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}
func bedrockCredentialModel(policy network.Policy) modelcfg.Model {
	return modelcfg.Model{Provider: "bedrock", ProviderID: "amazon-bedrock", AWSRegion: "us-east-1", BedrockAuth: "sigv4", Network: &policy}
}
func resolveRuntimeCredentials(ctx context.Context, source modelcfg.Model, cached bool) (modelcfg.Model, error) {
	var resolved modelcfg.Model
	var err error
	if cached {
		var cache CredentialCache
		resolved, _, err = cache.Resolve(ctx, source, "fixture")
	} else {
		resolved, _, err = ResolveRunCredentials(ctx, source, "fixture")
	}
	return resolved, err
}

func TestBedrockRuntimeIdentityRouting(t *testing.T) {
	for _, item := range []struct{ name, protocol, endpoint, config, relative, full string }{
		{name: "ec2-bedrock", protocol: "bedrock"},
		{name: "ec2-mantle-chat", protocol: "bedrock-mantle-chat"},
		{name: "ec2-mantle-responses", protocol: "bedrock-mantle-responses"},
		{name: "ec2-custom-endpoint", endpoint: "http://metadata.fixture.invalid"},
		{name: "ec2-shared-endpoint", config: "[default]\nec2_metadata_service_endpoint = http://metadata.fixture.invalid\n"},
		{name: "ec2-auto-defaults", config: "[default]\ndefaults_mode = auto\n"},
		{name: "ec2-ipv6", config: "[default]\nec2_metadata_service_endpoint_mode = IPv6\n"},
		{name: "container-relative", relative: "/container"},
		{name: "eks-full", full: "http://169.254.170.23/container"},
		{name: "eks-ipv6", full: "http://[fd00:ec2::23]/container"},
	} {
		t.Run(item.name, func(t *testing.T) {
			file := isolateRuntimeCredentials(t)
			f := newRuntimeCredentialFixture(t)
			if item.config != "" {
				if err := os.WriteFile(file, []byte(item.config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("AWS_EC2_METADATA_SERVICE_ENDPOINT", item.endpoint)
			t.Setenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", item.relative)
			t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", item.full)
			t.Setenv("AWS_CONTAINER_AUTHORIZATION_TOKEN", "fixture-container-auth")
			source := bedrockCredentialModel(f.policy)
			if item.protocol != "" {
				source.Provider = item.protocol
			}
			t.Run("rejected", func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_, err := resolveRuntimeCredentials(ctx, source, true)
				if !errors.Is(err, network.ErrRuntimeIdentityProxy) || len(f.observed()) != 0 {
					t.Fatalf("runtime identity crossed proxy boundary: err=%v requests=%v", err, f.observed())
				}
			})
			t.Run("direct", func(t *testing.T) {
				f.policy.Profile = nil
				source.Network = &f.policy
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				resolved, err := resolveRuntimeCredentials(ctx, source, false)
				if err != nil || resolved.AWSAccessID != "direct-runtime-id" {
					t.Fatalf("direct credentials failed: %v %+v requests=%v", err, resolved, f.observed())
				}
				for _, request := range f.observed() {
					if !strings.HasPrefix(request, "direct ") {
						t.Fatalf("metadata used proxy: %v", f.observed())
					}
				}
			})
		})
	}
}

func TestBedrockContainerUsesVistaTransport(t *testing.T) {
	isolateRuntimeCredentials(t)
	f := newRuntimeCredentialFixture(t)
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", f.origin.URL+"/container")
	t.Setenv("AWS_CONTAINER_AUTHORIZATION_TOKEN", "fixture-container-auth")
	resolved, err := resolveRuntimeCredentials(context.Background(), bedrockCredentialModel(network.Policy{}), false)
	if err != nil || resolved.AWSAccessID != "direct-runtime-id" {
		t.Fatalf("loopback container: %v", err)
	}
	f.mu.Lock()
	dials := f.dials
	f.mu.Unlock()
	if dials != 1 {
		t.Fatalf("container provider bypassed Vista transport: observed dials=%d", dials)
	}
}

func TestBedrockCustomHTTPSContainerRejectsProxy(t *testing.T) {
	isolateRuntimeCredentials(t)
	f := newRuntimeCredentialFixture(t)
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "https://container-agent.fixture.invalid/container")
	_, err := resolveRuntimeCredentials(context.Background(), bedrockCredentialModel(f.policy), false)
	if !errors.Is(err, network.ErrRuntimeIdentityProxy) || len(f.observed()) != 0 {
		t.Fatalf("HTTPS container endpoint escaped identity check: %v %v", err, f.observed())
	}
}

func TestBedrockCredentialPrecedenceAndRemoteExchange(t *testing.T) {
	t.Run("environment-with-auto-defaults", func(t *testing.T) {
		isolateRuntimeCredentials(t)
		f := newRuntimeCredentialFixture(t)
		t.Setenv("AWS_ACCESS_KEY_ID", "environment-id")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "environment-secret")
		t.Setenv("AWS_DEFAULTS_MODE", "auto")
		resolved, err := resolveRuntimeCredentials(context.Background(), bedrockCredentialModel(f.policy), false)
		if err != nil || resolved.AWSAccessID != "environment-id" || len(f.observed()) != 0 {
			t.Fatalf("credential precedence/auto metadata guard: %v %+v %v", err, resolved, f.observed())
		}
	})
	t.Run("disabled-metadata", func(t *testing.T) {
		isolateRuntimeCredentials(t)
		f := newRuntimeCredentialFixture(t)
		t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
		if _, err := resolveRuntimeCredentials(context.Background(), bedrockCredentialModel(network.Policy{}), false); err == nil || len(f.observed()) != 0 {
			t.Fatalf("metadata disable ignored: %v %v", err, f.observed())
		}
	})
	for _, credentialSource := range []string{"Ec2InstanceMetadata", "EcsContainer"} {
		t.Run("assume-role-"+credentialSource, func(t *testing.T) {
			file := isolateRuntimeCredentials(t)
			f := newRuntimeCredentialFixture(t)
			contents := "[default]\nrole_arn = arn:aws:iam::123456789012:role/fixture\ncredential_source = " + credentialSource + "\n"
			if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			f.policy.Profile = nil
			t.Setenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "/container")
			t.Setenv("AWS_CONTAINER_AUTHORIZATION_TOKEN", "fixture-container-auth")
			t.Setenv("AWS_ENDPOINT_URL_STS", "http://sts.fixture.invalid/aws-sts")
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			resolved, err := resolveRuntimeCredentials(ctx, bedrockCredentialModel(f.policy), false)
			if err != nil || resolved.AWSAccessID != "assumed-id" {
				t.Fatalf("AssumeRole: %v requests=%v", err, f.observed())
			}
			requests := f.observed()
			if len(requests) == 0 || requests[len(requests)-1] != "direct sts.fixture.invalid/aws-sts" {
				t.Fatalf("ordinary STS exchange lost direct connection: %v", requests)
			}
		})
	}
}

func writeVertexFederation(t *testing.T, aws, nested bool, tokenSource string) {
	t.Helper()
	source := map[string]any{"url": tokenSource}
	if aws {
		source = map[string]any{"environment_id": "aws1", "url": "http://metadata.fixture.invalid/roles", "region_url": "http://metadata.fixture.invalid/region", "imdsv2_session_token_url": "http://metadata.fixture.invalid/token", "regional_cred_verification_url": "https://sts.{region}.amazonaws.com?Action=GetCallerIdentity&Version=2011-06-15"}
	}
	config := map[string]any{"type": "external_account", "audience": "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/fixture/providers/aws", "subject_token_type": "urn:ietf:params:oauth:token-type:jwt", "token_url": "http://sts.fixture.invalid/google-sts", "credential_source": source}
	if aws {
		config["subject_token_type"] = "urn:ietf:params:aws:token-type:aws4_request"
	}
	if nested {
		config = map[string]any{"type": "impersonated_service_account", "service_account_impersonation_url": "http://sts.fixture.invalid/impersonate", "source_credentials": config}
	}
	writeRuntimeADC(t, config)
}

func writeRuntimeADC(t *testing.T, config any) {
	t.Helper()
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "adc.json")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", file)
}
func TestVertexFederatedRuntimeIdentityRouting(t *testing.T) {
	for _, item := range []struct {
		name        string
		aws, nested bool
		url         string
	}{
		{name: "aws", aws: true},
		{name: "nested-aws", aws: true, nested: true},
		{name: "azure-url", url: "http://169.254.169.254/subject-token"},
		{name: "gce-url", url: "http://metadata.google.internal/subject-token"},
	} {
		t.Run(item.name, func(t *testing.T) {
			isolateRuntimeCredentials(t)
			f := newRuntimeCredentialFixture(t)
			writeVertexFederation(t, item.aws, item.nested, item.url)
			source := modelcfg.Model{ProviderID: "google-vertex", Network: &f.policy}
			t.Run("rejected", func(t *testing.T) {
				_, err := resolveRuntimeCredentials(context.Background(), source, true)
				if !errors.Is(err, network.ErrRuntimeIdentityProxy) || len(f.observed()) != 0 {
					t.Fatalf("federation accepted proxy identity: %v %v", err, f.observed())
				}
			})
			t.Run("direct", func(t *testing.T) {
				f.policy.Profile = nil
				source.Network = &f.policy
				resolved, err := resolveRuntimeCredentials(context.Background(), source, false)
				want := "fixture-google-token"
				if item.nested {
					want = "fixture-impersonated-token"
				}
				if err != nil || resolved.APIKey != want {
					t.Fatalf("direct federation: %v requests=%v", err, f.observed())
				}
				for _, request := range f.observed() {
					if !strings.HasPrefix(request, "direct ") {
						t.Fatalf("direct federation used a proxy: %v", f.observed())
					}
				}
			})
		})
	}
	t.Run("ordinary-remote-url-source", func(t *testing.T) {
		isolateRuntimeCredentials(t)
		f := newRuntimeCredentialFixture(t)
		writeVertexFederation(t, false, false, "http://oidc.fixture.invalid/subject-token")
		resolved, err := resolveRuntimeCredentials(context.Background(), modelcfg.Model{ProviderID: "google-vertex", Network: &f.policy}, false)
		if err != nil || resolved.APIKey != "fixture-google-token" {
			t.Fatalf("remote subject token provider: %v", err)
		}
		requests := f.observed()
		if len(requests) != 2 || requests[0] != "proxy oidc.fixture.invalid/subject-token" || requests[1] != "proxy sts.fixture.invalid/google-sts" {
			t.Fatalf("remote URL/STS routes changed: %v", requests)
		}
	})
	t.Run("aws-environment-before-metadata", func(t *testing.T) {
		isolateRuntimeCredentials(t)
		f := newRuntimeCredentialFixture(t)
		writeVertexFederation(t, true, false, "")
		t.Setenv("AWS_ACCESS_KEY_ID", "environment-id")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "environment-secret")
		t.Setenv("AWS_REGION", "us-east-1")
		resolved, err := resolveRuntimeCredentials(context.Background(), modelcfg.Model{ProviderID: "google-vertex", Network: &f.policy}, false)
		requests := f.observed()
		if err != nil || resolved.APIKey != "fixture-google-token" || len(requests) != 1 || requests[0] != "proxy sts.fixture.invalid/google-sts" {
			t.Fatalf("unused metadata settings blocked environment federation: %v %v", err, requests)
		}
	})
	for _, headers := range []map[string]string{{"Metadata-Flavor": "Google"}, {"metadata": "true"}} {
		t.Run("custom-metadata-headers-"+fmt.Sprint(headers), func(t *testing.T) {
			isolateRuntimeCredentials(t)
			f := newRuntimeCredentialFixture(t)
			writeRuntimeADC(t, map[string]any{
				"type": "external_account", "audience": "fixture-audience", "subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
				"token_url":         "http://sts.fixture.invalid/google-sts",
				"credential_source": map[string]any{"url": "http://metadata.fixture.invalid/subject-token", "headers": headers},
			})
			source := modelcfg.Model{ProviderID: "google-vertex", Network: &f.policy}
			if _, err := resolveRuntimeCredentials(context.Background(), source, false); !errors.Is(err, network.ErrRuntimeIdentityProxy) || len(f.observed()) != 0 {
				t.Fatalf("custom metadata headers lost source policy: %v %v", err, f.observed())
			}
			f.policy.Profile = nil
			resolved, err := resolveRuntimeCredentials(context.Background(), source, false)
			if err != nil || resolved.APIKey != "fixture-google-token" {
				t.Fatalf("custom direct metadata failed: %v %v", err, f.observed())
			}
		})
	}
}

func TestRuntimeCredentialCancellationAndRedirect(t *testing.T) {
	for _, kind := range []string{"ec2", "container", "vertex-aws", "vertex-url"} {
		for _, action := range []string{"cancel", "redirect", "same-host-redirect"} {
			t.Run(kind+"-"+action, func(t *testing.T) {
				isolateRuntimeCredentials(t)
				f := newRuntimeCredentialFixture(t)
				policy := f.policy
				policy.Profile = nil
				source := bedrockCredentialModel(policy)
				path := "/latest/meta-data/iam/security-credentials/fixture-role"
				switch kind {
				case "container":
					t.Setenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "/container")
					t.Setenv("AWS_CONTAINER_AUTHORIZATION_TOKEN", "fixture-container-auth")
					path = "/container"
				case "vertex-aws":
					writeVertexFederation(t, true, false, "")
					source = modelcfg.Model{ProviderID: "google-vertex", Network: &policy}
					path = "/roles/fixture-role"
				case "vertex-url":
					writeVertexFederation(t, false, false, "http://169.254.169.254/subject-token")
					source = modelcfg.Model{ProviderID: "google-vertex", Network: &policy}
					path = "/subject-token"
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if action == "cancel" {
					f.waitPath.Store(&path)
					done := make(chan error, 1)
					go func() { _, err := resolveRuntimeCredentials(ctx, source, true); done <- err }()
					select {
					case <-f.started:
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
						t.Fatal("credential cache waited after cancellation")
					}
					select {
					case <-f.canceled:
					case <-time.After(time.Second):
						t.Fatal("SDK HTTP request remained active after cancellation")
					}
				} else {
					location := "http://sts.fixture.invalid/redirected-identity"
					if action == "same-host-redirect" {
						location = "/redirected-identity"
					}
					f.redirect.Store(&runtimeIdentityRedirect{path: path, location: location})
					resolved, err := resolveRuntimeCredentials(ctx, source, false)
					if err == nil || resolved.AWSAccessID != "" || resolved.APIKey != "" {
						t.Fatalf("metadata redirect credentials accepted: %v %+v", err, resolved)
					}
					for _, request := range f.observed() {
						if strings.Contains(request, "/redirected-identity") || strings.HasPrefix(request, "proxy ") {
							t.Fatalf("metadata redirect dispatched: %v", f.observed())
						}
					}
				}
			})
		}
	}
}
