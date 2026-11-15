package network

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntimeIdentityClientContract(t *testing.T) {
	var direct, proxied, opened atomic.Int32
	closed := make(chan struct{}, 32)
	started := make(chan struct{}, 1)
	endpoint := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		direct.Add(1)
		switch r.URL.Path {
		case "/metadata/redirect":
			http.Redirect(w, r, "http://exchange.fixture.invalid/", http.StatusFound)
		case "/metadata/same-host":
			http.Redirect(w, r, "/metadata/token", http.StatusTemporaryRedirect)
		case "/metadata/wait":
			started <- struct{}{}
			<-r.Context().Done()
		default:
			io.WriteString(w, "identity")
		}
	}))
	endpoint.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			opened.Add(1)
		case http.StateClosed:
			closed <- struct{}{}
		}
	}
	endpoint.Start()
	defer endpoint.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied.Add(1)
		switch r.URL.Path {
		case "/enter-metadata":
			http.Redirect(w, r, "http://identity.fixture.invalid/metadata/token", http.StatusFound)
		case "/redirect":
			http.Redirect(w, r, "/", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		default:
			io.WriteString(w, "exchange")
		}
	}))
	defer proxy.Close()
	entry, _ := url.Parse(proxy.URL)
	port, _ := strconv.Atoi(entry.Port())
	policy := Policy{Profile: &Profile{ID: "selected", Name: "Selected", Protocol: "http", Server: entry.Hostname(), Port: port, Authentication: "none"}}
	base := &http.Transport{DialContext: func(ctx context.Context, kind, address string) (net.Conn, error) {
		switch address {
		case proxy.Listener.Addr().String():
		case "identity.fixture.invalid:80":
			address = endpoint.Listener.Addr().String()
		default:
			return nil, errors.New("unexpected isolated target: " + address)
		}
		return new(net.Dialer).DialContext(ctx, kind, address)
	}}
	defer (Transport{Base: base}).CloseIdleConnections()
	match := func(r *http.Request) bool { return r.URL.Hostname() == "identity.fixture.invalid" }
	makeClient := func(ctx context.Context, options http.Client, predicate func(*http.Request) bool) *http.Client {
		t.Helper()
		options.Transport = base
		client, err := ClientFor(ctx, options, ConnectionScope)
		if err != nil {
			t.Fatal(err)
		}
		guarded, err := NewRuntimeIdentityClient(ctx, client, predicate)
		if err != nil {
			t.Fatal(err)
		}
		if guarded.Timeout != options.Timeout || client.CheckRedirect != nil && guarded.CheckRedirect == nil {
			t.Fatal("client options lost")
		}
		return guarded.Client
	}
	ctx := WithConnectionPolicy(context.Background(), policy)
	blocked := makeClient(ctx, http.Client{}, match)
	if _, err := blocked.Get("http://identity.fixture.invalid/metadata/token"); !errors.Is(err, ErrRuntimeIdentityProxy) {
		t.Fatalf("proxy identity accepted: %v", err)
	}
	if direct.Load() != 0 || proxied.Load() != 0 {
		t.Fatal("rejected identity request was dispatched")
	}
	consume := func(client *http.Client, target string, want int) {
		t.Helper()
		response, err := client.Get(target)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		io.Copy(io.Discard, response.Body)
		if response.StatusCode != want {
			t.Fatalf("status=%d want=%d", response.StatusCode, want)
		}
	}
	consume(blocked, "http://exchange.fixture.invalid/redirect", 200)
	if proxied.Load() != 2 {
		t.Fatal("ordinary exchanges lost their proxy/redirect behavior")
	}
	consume(blocked, "http://exchange.fixture.invalid/enter-metadata", 302)
	if direct.Load() != 0 || proxied.Load() != 3 {
		t.Fatal("redirect entered metadata source")
	}
	if _, err := blocked.Get("http://exchange.fixture.invalid/loop"); err == nil || !strings.Contains(err.Error(), "10 redirects") {
		t.Fatalf("default redirect limit lost: %v", err)
	}
	var redirectCalls atomic.Int32
	custom := makeClient(ctx, http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { redirectCalls.Add(1); return http.ErrUseLastResponse }}, match)
	consume(custom, "http://exchange.fixture.invalid/redirect", 302)
	if redirectCalls.Load() != 1 {
		t.Fatal("ordinary redirect callback lost")
	}
	policy.Profile = nil
	directClient := makeClient(WithConnectionPolicy(context.Background(), policy), http.Client{Timeout: time.Second}, nil)
	for range 2 {
		consume(directClient, "http://identity.fixture.invalid/metadata/token", 200)
	}
	if opened.Load() != 1 {
		t.Fatalf("metadata reuse lost: %d", opened.Load())
	}
	proxyBefore := proxied.Load()
	consume(directClient, "http://identity.fixture.invalid/metadata/redirect", 302)
	consume(directClient, "http://identity.fixture.invalid/metadata/same-host", 307)
	if direct.Load() != 4 || proxied.Load() != proxyBefore {
		t.Fatal("metadata redirect dispatched")
	}
	directClient.CloseIdleConnections()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("guarded client did not release idle pools")
	}
	// The client must pin cancellation even when an SDK passes a background request.
	cancelCtx, cancel := context.WithCancel(WithConnectionPolicy(context.Background(), Policy{}))
	client := makeClient(cancelCtx, http.Client{}, nil)
	done := make(chan error, 1)
	go func() { _, err := client.Get("http://identity.fixture.invalid/metadata/wait"); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("identity request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("identity request outlived cancellation")
	}
	if _, err := NewRuntimeIdentityClient(context.Background(), blocked, nil); err == nil {
		t.Fatal("accepted missing connection policy")
	}
}
