package network

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/oauth2"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type observedTransport struct{ base http.RoundTripper }

func TestSanitizedProxyErrorSemantics(t *testing.T) {
	policy := Policy{Profile: &Profile{ID: "proxy", Name: "Proxy", Protocol: "http", Server: "entry.test", Port: 8080, Authentication: "password", Username: "private-user", Password: "private-secret"}}
	sentinel := errors.New("private-secret at " + policy.Profile.URL().String())
	for _, test := range []struct {
		name      string
		cause     error
		timeout   bool
		temporary bool
	}{
		{"timeout", &net.DNSError{Err: sentinel.Error(), IsTimeout: true, IsTemporary: true}, true, true},
		{"deadline", context.DeadlineExceeded, true, true},
		{"cancelled", context.Canceled, false, false},
		{"ordinary", sentinel, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := Sanitize(fmt.Errorf("proxy request: %w", test.cause), policy)
			caller := &url.Error{Op: "Get", URL: "http://origin.test", Err: err}
			if caller.Timeout() != test.timeout || caller.Temporary() != test.temporary || !errors.Is(caller, test.cause) {
				t.Fatalf("caller semantics lost: timeout=%v temporary=%v cause=%v", caller.Timeout(), caller.Temporary(), errors.Is(caller, test.cause))
			}
			if strings.Contains(caller.Error(), "private-secret") || strings.Contains(caller.Error(), "private-user") || strings.Contains(caller.Error(), policy.Profile.URL().String()) {
				t.Fatal("sanitized error disclosed proxy credentials")
			}
			if original, ok := test.cause.(*net.DNSError); ok {
				var recovered *net.DNSError
				if !errors.As(caller, &recovered) || recovered != original {
					t.Fatal("concrete error identity lost")
				}
			}
		})
	}
}

func (t observedTransport) Unwrap() http.RoundTripper { return t.base }
func (t observedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Wrapper", "retained")
	return t.base.RoundTrip(r)
}

type unknownTransport struct{ called *atomic.Int32 }

func (t unknownTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.called.Add(1)
	return nil, errors.New("unknown transport was called")
}

func TestScopedClientContract(t *testing.T) {
	var requests, opened, closed atomic.Int32
	closedSignal := make(chan struct{}, 8)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("X-Wrapper") != "retained" {
			t.Error("wrapper lost")
		}
		switch r.URL.Path {
		case "/wait":
			<-r.Context().Done()
		case "/redirect":
			http.Redirect(w, r, "/", http.StatusFound)
		default:
			io.WriteString(w, "ok")
		}
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			opened.Add(1)
		}
		if state == http.StateClosed {
			closed.Add(1)
			closedSignal <- struct{}{}
		}
	}
	server.Start()
	defer server.Close()
	base := http.DefaultTransport.(*http.Transport).Clone()
	leaf := &Transport{Base: base, Scope: ConnectionScope}
	defer leaf.CloseIdleConnections()
	jar, _ := cookiejar.New(nil)
	original := http.Client{Transport: observedTransport{base: leaf}, Timeout: time.Second, Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	client := NewClient(original, ConnectionScope)
	if client.Timeout != original.Timeout || client.Jar != jar || original.Transport == client.Transport {
		t.Fatal("client options or original ownership changed")
	}
	call := func(ctx context.Context, path string) (*http.Response, error) {
		r, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
		return client.Do(r)
	}
	for _, ctx := range []context.Context{context.Background(), WithPolicy(context.Background(), Policy{})} {
		if _, err := call(ctx, "/"); err == nil || !strings.Contains(err.Error(), "policy is missing") {
			t.Fatalf("missing connection scope accepted: %v", err)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("unbound request reached server")
	}
	ctx := WithConnectionPolicy(context.Background(), Policy{})
	for range 2 {
		response, err := call(ctx, "/")
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}
	if opened.Load() != 1 {
		t.Fatalf("reuse lost: %d connections", opened.Load())
	}
	response, err := call(ctx, "/redirect")
	if err != nil || response.StatusCode != http.StatusFound {
		t.Fatalf("redirect policy lost: %v", err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	client.CloseIdleConnections()
	select {
	case <-closedSignal:
	case <-time.After(time.Second):
		t.Fatal("wrapped pool did not close")
	}
	if closed.Load() != 1 {
		t.Fatal("unexpected close count")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := call(cancelled, "/"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	short := NewClient(original, ConnectionScope)
	short.Timeout = 25 * time.Millisecond
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/wait", nil)
	if _, err := short.Do(r); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("client timeout lost: %v", err)
	}
	var called atomic.Int32
	unsupported := NewClient(http.Client{Transport: unknownTransport{&called}}, ConnectionScope)
	r, _ = http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if _, err := unsupported.Do(r); err == nil || called.Load() != 0 {
		t.Fatal("unknown wrapper bypassed policy validation")
	}
}

func TestSDKClientPinsPolicyAndCancellation(t *testing.T) {
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wait" {
			io.Copy(io.Discard, r.Body)
			started <- struct{}{}
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"device_code":"code","user_code":"user","verification_uri":"https://verify.invalid","expires_in":60}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(WithConnectionPolicy(context.Background(), Policy{}))
	defer cancel()
	client, err := ClientFor(ctx, *server.Client(), ConnectionScope)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	config := oauth2.Config{ClientID: "fixture", Endpoint: oauth2.Endpoint{DeviceAuthURL: server.URL}}
	if _, err := config.DeviceAuth(context.WithValue(ctx, oauth2.HTTPClient, client)); err != nil {
		t.Fatal(err)
	}
	config.Endpoint.DeviceAuthURL += "/wait"
	finished := make(chan error, 1)
	go func() {
		_, err := config.DeviceAuth(context.WithValue(ctx, oauth2.HTTPClient, client))
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("SDK request did not start")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("caller cancellation lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SDK request outlived operation")
	}
}

func TestConnectionPolicySnapshotsArePrivate(t *testing.T) {
	profile := Profile{ID: "saved", Name: "Saved", Protocol: "http", Server: "proxy.invalid", Port: 8080, Authentication: "password", Username: "original", Password: "secret"}
	ctx := WithConnectionPolicy(context.Background(), Policy{Profile: &profile})
	profile.Server, profile.Username = "edited.invalid", "*"
	first, err := ConnectionPolicy(ctx)
	if err != nil || first.Profile.Server != "proxy.invalid" || first.Profile.Username != "original" {
		t.Fatal("caller changed a bound snapshot")
	}
	first.Profile.Server, first.Profile.Username = "changed.invalid", "*"
	second, _ := ConnectionPolicy(ctx)
	if second.Profile.Server != "proxy.invalid" || second.Profile.Username != "original" {
		t.Fatal("reading a policy exposed mutable snapshot storage")
	}
	if _, err := ConnectionPolicy(WithPolicy(ctx, Policy{})); err == nil {
		t.Fatal("an application policy retained connection scope")
	}
}
