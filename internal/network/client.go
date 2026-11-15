package network

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
)

// Scope distinguishes application traffic from requests owned by a model connection.
type Scope uint8

const (
	ApplicationScope Scope = iota
	ConnectionScope
)

type scopeKey struct{}

func connectionScoped(ctx context.Context) bool {
	return ctx.Value(scopeKey{}) == ConnectionScope
}

func clonePolicy(p Policy) Policy {
	if p.Profile != nil {
		profile := *p.Profile
		p.Profile = &profile
	}
	return p
}

// WithConnectionPolicy restores a private connection snapshot, including Direct.
func WithConnectionPolicy(ctx context.Context, policy Policy) context.Context {
	return context.WithValue(WithPolicy(ctx, policy), scopeKey{}, ConnectionScope)
}

// ConnectionPolicy never substitutes the application's global route.
func ConnectionPolicy(ctx context.Context) (Policy, error) {
	if !connectionScoped(ctx) {
		return Policy{}, errors.New("model connection network policy is missing")
	}
	return FromContext(ctx)
}

func (t Transport) requestPolicy(ctx context.Context) (Policy, error) {
	if t.Policy != nil {
		return clonePolicy(*t.Policy), t.Policy.Validate()
	}
	if t.Scope == ConnectionScope {
		return ConnectionPolicy(ctx)
	}
	return FromContext(ctx)
}

// NewClient copies client options and connects native HTTP leaves to application
// egress. Wrappers expose Unwrap; an unknown chain is rejected before dispatch.
func NewClient(options http.Client, scope Scope) *http.Client {
	options.Transport = HTTPTransport(options.Transport, scope)
	return &options
}

// HTTPTransport is also the injection point for SDKs and transport wrappers.
func HTTPTransport(base http.RoundTripper, scope Scope) http.RoundTripper {
	switch base.(type) {
	case nil, *http.Transport:
		return Transport{Base: base, Scope: scope}
	default:
		return clientTransport{base: base, err: checkTransport(base, scope)}
	}
}

func checkTransport(base http.RoundTripper, scope Scope) error {
	for range 16 {
		switch t := base.(type) {
		case Transport:
			if scope == ConnectionScope && t.Scope != ConnectionScope && t.Policy == nil {
				return errors.New("HTTP transport requires a connection-scoped network leaf")
			}
			return nil
		case *Transport:
			if t != nil {
				base = *t
				continue
			}
		}
		wrapper, ok := base.(interface{ Unwrap() http.RoundTripper })
		if !ok {
			break
		}
		base = wrapper.Unwrap()
	}
	return errors.New("HTTP transport is not connected to application network policy")
}

type clientTransport struct {
	base http.RoundTripper
	err  error
}

func (t clientTransport) Unwrap() http.RoundTripper { return t.base }
func (t clientTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.err != nil {
		return nil, t.err
	}
	return t.base.RoundTrip(r)
}
func (t clientTransport) CloseIdleConnections() { closeIdleConnections(t.base) }

func closeIdleConnections(base http.RoundTripper) {
	for range 16 {
		if closer, ok := base.(interface{ CloseIdleConnections() }); ok {
			closer.CloseIdleConnections()
			return
		}
		wrapper, ok := base.(interface{ Unwrap() http.RoundTripper })
		if !ok {
			return
		}
		base = wrapper.Unwrap()
	}
}

// ClientFor pins policy and operation lifetime when an SDK constructs requests
// without carrying its caller context (including oauth2 v0.35 DeviceAuth).
func ClientFor(ctx context.Context, options http.Client, scope Scope) (*http.Client, error) {
	var policy Policy
	var err error
	if scope == ConnectionScope {
		policy, err = ConnectionPolicy(ctx)
	} else {
		policy, err = FromContext(ctx)
	}
	if err != nil {
		return nil, err
	}
	client := NewClient(options, scope)
	client.Transport = operationTransport{base: client.Transport, ctx: ctx, policy: policy, scope: scope}
	return client, nil
}

type operationTransport struct {
	base   http.RoundTripper
	ctx    context.Context
	policy Policy
	scope  Scope
}

func (t operationTransport) Unwrap() http.RoundTripper { return t.base }
func (t operationTransport) CloseIdleConnections()     { closeIdleConnections(t.base) }
func (t operationTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx := r.Context()
	var cancel context.CancelFunc
	if deadline, ok := t.ctx.Deadline(); ok {
		ctx, cancel = context.WithDeadline(ctx, deadline)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	stop := context.AfterFunc(t.ctx, cancel)
	finish := func() { stop(); cancel() }
	if err := t.ctx.Err(); err != nil {
		finish()
		return nil, err
	}
	if t.scope == ConnectionScope {
		ctx = WithConnectionPolicy(ctx, t.policy)
	} else {
		ctx = WithPolicy(ctx, t.policy)
	}
	response, err := t.base.RoundTrip(r.Clone(ctx))
	if err != nil {
		finish()
		return nil, err
	}
	response.Body = &operationBody{ReadCloser: response.Body, finish: finish}
	return response, nil
}

type operationBody struct {
	io.ReadCloser
	once   sync.Once
	finish func()
}

func (b *operationBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(b.finish)
	}
	return n, err
}
func (b *operationBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.finish)
	return err
}
