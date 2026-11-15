package provider

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/network"
)

func TestVertexDefaultCredentialCancellation(t *testing.T) {
	// Keep a developer's ambient gcloud credentials out of the GCE fixture.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
	if runtime.GOOS == "windows" {
		file = filepath.Join(os.Getenv("APPDATA"), "gcloud", "application_default_credentials.json")
	}
	if _, err := os.Stat(file); err == nil {
		t.Skip("GCE fixture requires an environment without ambient gcloud credentials")
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	for _, item := range []struct {
		name   string
		cached bool
		file   bool
	}{{"gce-resolver", false, false}, {"gce-cache", true, false}, {"oauth-resolver", false, true}, {"oauth-cache", true, true}} {
		t.Run(item.name, func(t *testing.T) {
			started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/token") {
					io.WriteString(w, "fixture-project")
					return
				}
				if !item.file && (r.Header.Get("Metadata-Flavor") != "Google" || r.URL.Query().Get("scopes") != "https://www.googleapis.com/auth/cloud-platform") {
					t.Error("GCE token identity or scopes changed")
				}
				io.Copy(io.Discard, r.Body)
				close(started)
				select {
				case <-r.Context().Done():
					close(canceled)
				case <-release:
					io.WriteString(w, `{"access_token":"fixture-token","expires_in":3600,"token_type":"Bearer"}`)
				}
			}))
			t.Cleanup(func() { close(release); server.Close() })
			t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(server.URL, "http://"))
			if item.file {
				key, err := rsa.GenerateKey(rand.Reader, 2048)
				if err != nil {
					t.Fatal(err)
				}
				data, _ := json.Marshal(map[string]string{
					"type": "service_account", "client_email": "fixture@example.invalid", "token_uri": server.URL + "/token",
					"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
				})
				file := filepath.Join(t.TempDir(), "credentials.json")
				if err := os.WriteFile(file, data, 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", file)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				source := modelcfg.Model{ProviderID: "google-vertex"}
				var err error
				if item.cached {
					var cache CredentialCache
					_, _, err = cache.Resolve(ctx, source, "fixture")
				} else {
					_, _, err = ResolveRunCredentials(ctx, source, "fixture")
				}
				done <- err
			}()
			select {
			case <-started:
			case err := <-done:
				t.Fatalf("GCE request did not start: %v", err)
			case <-time.After(2 * time.Second):
				t.Fatal("GCE request did not start")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation identity lost: %v", err)
				}
			case <-time.After(300 * time.Millisecond):
				t.Fatal("GCE credential request waited for the upstream after cancellation")
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("metadata HTTP request remained active after cancellation")
			}
		})
	}
}

func TestVertexMetadataProxyAndFilePrecedence(t *testing.T) {
	var selected, global, metadataCalls, redirectStatus atomic.Int32
	var sameHostRedirect atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selected.Add(1)
		if r.Host != "metadata-redirect.fixture.invalid" {
			t.Errorf("unexpected proxy target: %s", r.Host)
		}
		io.WriteString(w, `{"access_token":"proxy-platform-token","expires_in":3600,"token_type":"Bearer"}`)
	}))
	defer proxy.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		global.Add(1)
		http.Error(w, "wrong proxy", http.StatusBadGateway)
	}))
	defer other.Close()
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metadataCalls.Add(1)
		if (r.Host != "metadata.fixture.invalid" && r.Host != "169.254.169.254") || r.Header.Get("Metadata-Flavor") != "Google" || !strings.HasSuffix(r.URL.Path, "/token") {
			t.Error("unexpected metadata request or extra platform probe")
		}
		if status := redirectStatus.Load(); status != 0 {
			location := "http://metadata-redirect.fixture.invalid/token"
			if sameHostRedirect.Load() {
				location = r.URL.RequestURI()
			}
			w.Header().Set("Location", location)
			w.WriteHeader(int(status))
			io.WriteString(w, `{"access_token":"redirect-body-token","expires_in":3600,"token_type":"Bearer"}`)
			return
		}
		io.WriteString(w, `{"access_token":"fixture-token","expires_in":3600,"token_type":"Bearer"}`)
	}))
	defer endpoint.Close()
	previous := http.DefaultTransport
	base := &http.Transport{DialContext: func(ctx context.Context, kind, address string) (net.Conn, error) {
		if address == proxy.Listener.Addr().String() || address == other.Listener.Addr().String() {
			return new(net.Dialer).DialContext(ctx, kind, address)
		}
		if address != "metadata.fixture.invalid:80" && address != "169.254.169.254:80" {
			t.Errorf("unexpected direct target: %s", address)
			return nil, errors.New("unexpected fixture target")
		}
		// Intercept before dialing: only the local fixture is ever contacted.
		return new(net.Dialer).DialContext(ctx, kind, endpoint.Listener.Addr().String())
	}}
	http.DefaultTransport = base
	t.Cleanup(func() {
		(network.Transport{Base: base}).CloseIdleConnections()
		http.DefaultTransport = previous
	})
	var profiles []network.Profile
	for i, endpoint := range []string{other.URL, proxy.URL} {
		u, _ := url.Parse(endpoint)
		port, _ := strconv.Atoi(u.Port())
		profiles = append(profiles, network.Profile{ID: strconv.Itoa(i), Name: "Fixture", Protocol: "http", Server: u.Hostname(), Port: port, Authentication: "none"})
	}
	settings := network.Settings{GlobalProxy: profiles[0].ID, Profiles: profiles}
	policy, _ := settings.Select(settings.GlobalProxy)
	ctx := network.WithPolicy(network.WithSettings(context.Background(), settings), policy)
	source := modelcfg.Model{ProviderID: "google-vertex", ProxyID: profiles[1].ID}
	t.Setenv("GCE_METADATA_HOST", "metadata.fixture.invalid")
	// No ambient file may redirect this fixture into a real credential exchange.
	if data, err := vertexADCJSON(); err != nil || data != nil {
		t.Skip("metadata proxy fixture requires an environment without ambient Google credentials")
	}
	var cache CredentialCache
	if _, _, err := cache.Resolve(ctx, source, "fixture"); !errors.Is(err, errVertexMetadataRoute) || metadataCalls.Load() != 0 || selected.Load() != 0 || global.Load() != 0 {
		t.Fatalf("metadata proxy route was not rejected before dispatch: %v", err)
	}
	source.ProxyID = ""
	ctx = network.WithPolicy(network.WithSettings(context.Background(), settings), policy)
	model, _, err := cache.Resolve(ctx, source, "fixture")
	if err != nil || model.APIKey != "fixture-token" || !model.CredentialExpiresAt.After(time.Now()) || metadataCalls.Load() != 1 {
		t.Fatalf("direct metadata token failed: %v", err)
	}
	t.Setenv("GCE_METADATA_HOST", "")
	if _, _, err := ResolveRunCredentials(ctx, source, "fixture"); err != nil || metadataCalls.Load() != 2 {
		t.Fatalf("default metadata lookup added a platform probe or failed: %v", err)
	}
	file := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(file, []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", file)
	if _, _, err := ResolveRunCredentials(ctx, source, "fixture"); err == nil || errors.Is(err, errVertexMetadataRoute) || metadataCalls.Load() != 2 {
		t.Fatal("invalid explicit credentials fell back to metadata")
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", file+".missing")
	if _, _, err := ResolveRunCredentials(ctx, source, "fixture"); err == nil || errors.Is(err, errVertexMetadataRoute) || metadataCalls.Load() != 2 {
		t.Fatal("missing explicit credentials fell back to metadata")
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := ResolveRunCredentials(canceled, source, "fixture"); !errors.Is(err, context.Canceled) || metadataCalls.Load() != 2 {
		t.Fatal("an already canceled operation dispatched metadata HTTP")
	}
	if selected.Load() != 0 || global.Load() != 0 {
		t.Fatal("metadata accessed a proxy's platform identity")
	}
	for _, redirect := range []struct {
		name     string
		code     int
		sameHost bool
	}{
		{"301", 301, false}, {"302", 302, false}, {"303", 303, false},
		{"307", 307, false}, {"308", 308, false}, {"302-same-host", 302, true},
	} {
		t.Run("redirect-"+redirect.name, func(t *testing.T) {
			redirectStatus.Store(int32(redirect.code))
			sameHostRedirect.Store(redirect.sameHost)
			for _, entry := range []struct {
				name   string
				cached bool
			}{{"resolver", false}, {"cache", true}} {
				t.Run(entry.name, func(t *testing.T) {
					before := metadataCalls.Load()
					var resolved modelcfg.Model
					var err error
					if entry.cached {
						var freshCache CredentialCache
						resolved, _, err = freshCache.Resolve(ctx, source, "fixture")
					} else {
						resolved, _, err = ResolveRunCredentials(ctx, source, "fixture")
					}
					if err == nil || resolved.APIKey != "" {
						t.Fatalf("metadata redirect accepted: token=%q err=%v proxyRequests=%d", resolved.APIKey, err, selected.Load())
					}
					if metadataCalls.Load() != before+1 || selected.Load() != 0 || global.Load() != 0 {
						t.Fatalf("metadata redirect dispatched or retried: metadata=%d selectedProxy=%d globalProxy=%d", metadataCalls.Load()-before, selected.Load(), global.Load())
					}
				})
			}
		})
	}
}
