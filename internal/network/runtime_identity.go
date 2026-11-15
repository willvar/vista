package network

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
)

// ErrRuntimeIdentityProxy rejects a proxy's identity as this runtime's identity.
var ErrRuntimeIdentityProxy = runtimeIdentityProxyError{}

type runtimeIdentityProxyError struct{}

func (runtimeIdentityProxyError) Error() string {
	return "runtime metadata credentials require a direct connection; clear the selected proxy or use explicit credentials"
}

// A route policy failure cannot be repaired by an SDK retry.
func (runtimeIdentityProxyError) RetryableError() bool { return false }

// RuntimeIdentityClient retains the HTTP client and an operation-local policy
// failure indicator for SDKs that do not preserve wrapped error identities.
type RuntimeIdentityClient struct {
	*http.Client
	guard *runtimeIdentityTransport
}

// RouteError preserves policy failures when an SDK flattens HTTP errors into
// strings. It records no endpoint, headers or credential material.
func (c *RuntimeIdentityClient) RouteError() error {
	if c.guard.blocked.Load() {
		return ErrRuntimeIdentityProxy
	}
	return nil
}

// NewRuntimeIdentityClient copies an operation-bound connection client and guards
// identity-source requests. A nil match guards every request. SDK adapters select
// sources by purpose; normal remote authorization retains its connection route.
// The copy shares pool ownership with client, including CloseIdleConnections.
func NewRuntimeIdentityClient(ctx context.Context, client *http.Client, match func(*http.Request) bool) (*RuntimeIdentityClient, error) {
	policy, err := ConnectionPolicy(ctx)
	if err != nil {
		return nil, err
	}
	if err := checkTransport(client.Transport, ConnectionScope); err != nil {
		return nil, err
	}
	if match == nil {
		match = func(*http.Request) bool { return true }
	}
	options := *client
	redirect := options.CheckRedirect
	guard := &runtimeIdentityTransport{base: client.Transport, policy: policy, match: match}
	options.Transport = guard
	options.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		// Guard both leaving a source and entering one from a remote exchange.
		// Never let redirects change a checked runtime-identity endpoint.
		if match(r) || match(via[len(via)-1]) {
			return http.ErrUseLastResponse
		}
		if redirect != nil {
			return redirect(r, via)
		}
		if len(via) >= 10 { // Preserve net/http's default redirect limit.
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &RuntimeIdentityClient{Client: &options, guard: guard}, nil
}

type runtimeIdentityTransport struct {
	base    http.RoundTripper
	policy  Policy
	match   func(*http.Request) bool
	blocked atomic.Bool
}

func (t *runtimeIdentityTransport) Unwrap() http.RoundTripper { return t.base }
func (t *runtimeIdentityTransport) CloseIdleConnections()     { closeIdleConnections(t.base) }
func (t *runtimeIdentityTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.match(r) && t.policy.Effective(r.URL).Profile != nil {
		t.blocked.Store(true)
		return nil, ErrRuntimeIdentityProxy
	}
	return t.base.RoundTrip(r)
}
