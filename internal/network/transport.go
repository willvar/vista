package network

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// Transport keeps callers' timeout/TLS configuration and test transports while
// replacing only application egress. It never consults proxy environment state.
type Transport struct {
	Base   http.RoundTripper
	Policy *Policy
	Scope  Scope
}
type engine struct {
	policy    Policy
	base      *http.Transport
	transport *http.Transport
}

var engines = struct {
	sync.Mutex
	values map[string]*engine
	order  []string
}{values: map[string]*engine{}}

func engineFor(p Policy, base *http.Transport) *engine {
	key := fmt.Sprintf("%p:%s", base, Fingerprint(p))
	engines.Lock()
	defer engines.Unlock()
	if e := engines.values[key]; e != nil {
		return e
	}
	e := &engine{policy: p, base: base}
	t := base.Clone()
	t.Proxy = nil
	t.ProxyConnectHeader = nil
	t.GetProxyConnectHeader = nil
	t.DialTLS = nil
	t.DialTLSContext = nil
	t.DialContext = e.dial
	previousConnect := t.OnProxyConnectResponse
	t.OnProxyConnectResponse = func(ctx context.Context, u *url.URL, req *http.Request, res *http.Response) error {
		if res.StatusCode == http.StatusProxyAuthRequired {
			return atStage("proxy-auth", fmt.Errorf("proxy returned HTTP %d", res.StatusCode))
		}
		if res.StatusCode != 200 {
			return atStage("proxy-connect", fmt.Errorf("proxy returned HTTP %d", res.StatusCode))
		}
		if previousConnect != nil {
			return previousConnect(ctx, u, req, res)
		}
		return nil
	}
	if p.Profile != nil {
		u := p.Profile.URL()
		t.Proxy = http.ProxyURL(u)
		t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			if p.Profile.ConnectIP != "" {
				address = net.JoinHostPort(p.Profile.ConnectIP, fmt.Sprint(p.Profile.Port))
			}
			return e.direct(ctx, network, address)
		}
		if p.Profile.Protocol == "https" && (p.Profile.InsecureSkipVerify || base.TLSClientConfig != nil && base.TLSClientConfig.InsecureSkipVerify) {
			// The native TLS config also verifies the destination after CONNECT.
			// Override only the proxy's first TLS hop; net/http still owns tunneling.
			t.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				conn, err := t.DialContext(ctx, network, address)
				if err != nil {
					return nil, err
				}
				secured, err := e.proxyTLS(ctx, conn)
				if err != nil {
					conn.Close()
				}
				return secured, err
			}
		}
	}
	e.transport = t
	if len(engines.order) >= 64 {
		old := engines.order[0]
		engines.order = engines.order[1:]
		if v := engines.values[old]; v != nil {
			v.transport.CloseIdleConnections()
		}
		delete(engines.values, old)
	}
	engines.values[key] = e
	engines.order = append(engines.order, key)
	return e
}
func (t Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	p, err := t.requestPolicy(r.Context())
	if err != nil {
		return nil, err
	}
	leaf, ok := base.(*http.Transport)
	if !ok {
		return base.RoundTrip(r)
	}
	p = p.Effective(r.URL)
	_, hasPins, err := pinnedAddresses(r.Context(), r.URL.Hostname())
	if err != nil {
		return nil, err
	}
	e := engineFor(p, leaf)
	// A resolved-IP tunnel preserves both the actual destination and HTTP Host.
	// Absolute-form HTTP forwarding cannot reliably keep those independent.
	selected := e.transport
	if hasPins {
		selected = e.boundTransport(r.Context().Value(addressKey{}).(addressSet))
	}
	response, err := selected.RoundTrip(r)
	if p.Profile != nil && response != nil && response.StatusCode == http.StatusProxyAuthRequired {
		response.Body.Close()
		return nil, atStage("proxy-auth", errors.New("proxy returned HTTP 407"))
	}
	return response, Sanitize(err, p)
}
func (t Transport) CloseIdleConnections() {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	if closer, ok := base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
	leaf, ok := base.(*http.Transport)
	if !ok {
		return
	}
	engines.Lock()
	defer engines.Unlock()
	for _, e := range engines.values {
		if e.base == leaf {
			e.transport.CloseIdleConnections()
		}
	}
	boundTransports.Lock()
	defer boundTransports.Unlock()
	for key, pool := range boundTransports.values {
		if key.engine.base == leaf {
			pool.CloseIdleConnections()
		}
	}
}

// Do preserves client settings and validates the network leaf beneath wrappers.
func Do(client *http.Client, r *http.Request) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	return NewClient(*client, ApplicationScope).Do(r)
}
func (e *engine) direct(ctx context.Context, network, address string) (net.Conn, error) {
	if e.base.DialContext != nil {
		return e.base.DialContext(ctx, network, address)
	}
	d := net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return d.DialContext(ctx, network, address)
}
func (e *engine) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, pinned, err := pinnedAddresses(ctx, host)
	if err != nil {
		return nil, err
	}
	if !pinned && loopbackHost(host) {
		ips = loopbackAddresses()
	} else if !pinned {
		return e.through(ctx, network, address)
	}
	return e.dialAddresses(ctx, network, host, port, ips)
}
func (e *engine) through(ctx context.Context, network, address string) (net.Conn, error) {
	p := e.policy.Profile
	if p == nil {
		return e.direct(ctx, network, address)
	}
	entry := p.Server
	if p.ConnectIP != "" {
		entry = p.ConnectIP
	}
	entry = net.JoinHostPort(entry, fmt.Sprint(p.Port))
	if p.Protocol == "socks5" {
		var auth *proxy.Auth
		if p.Authentication == "password" {
			auth = &proxy.Auth{User: p.Username, Password: p.Password}
		}
		dialer, err := proxy.SOCKS5("tcp", entry, auth, proxyDialFunc(e.direct))
		if err != nil {
			return nil, err
		}
		return dialer.(proxy.ContextDialer).DialContext(ctx, network, address)
	}
	conn, err := e.direct(ctx, "tcp", entry)
	if err != nil {
		return nil, atStage("proxy-connect", err)
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	deadline := time.Now().Add(30 * time.Second)
	if d, yes := ctx.Deadline(); yes && d.Before(deadline) {
		deadline = d
	}
	conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if p.Protocol == "https" {
		secured, err := e.proxyTLS(ctx, conn)
		if err != nil {
			return nil, err
		}
		conn = secured
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: address}, Host: address, Header: make(http.Header)}
	if p.Authentication == "password" {
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(p.Username+":"+p.Password)))
	}
	if err := req.Write(conn); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(io.LimitReader(conn, 64<<10))
	response, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		stage := "proxy-connect"
		if response.StatusCode == http.StatusProxyAuthRequired {
			stage = "proxy-auth"
		}
		return nil, atStage(stage, fmt.Errorf("proxy CONNECT returned HTTP %d", response.StatusCode))
	}
	if reader.Buffered() != 0 {
		return nil, errors.New("proxy sent unexpected CONNECT payload")
	}
	conn.SetDeadline(time.Time{})
	ok = true
	return conn, nil
}

// proxyTLS borrows the socket; its caller closes it on handshake failure.
func (e *engine) proxyTLS(ctx context.Context, conn net.Conn) (net.Conn, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if e.base.TLSClientConfig != nil {
		config = e.base.TLSClientConfig.Clone()
	}
	config.Certificates = nil
	config.GetClientCertificate = nil
	config.ServerName = e.policy.Profile.Server
	config.InsecureSkipVerify = e.policy.Profile.InsecureSkipVerify
	config.NextProtos = []string{"http/1.1"}
	tlsConn := tls.Client(conn, config)
	// Custom proxy handshakes run outside net/http's TLS timer.
	handshakeCtx := ctx
	cancel := func() {}
	if timeout := e.base.TLSHandshakeTimeout; timeout != 0 {
		handshakeCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	err := tlsConn.HandshakeContext(handshakeCtx)
	if errors.Is(err, context.DeadlineExceeded) && handshakeCtx.Err() != nil && ctx.Err() == nil {
		err = fmt.Errorf("TLS handshake timeout: %w", err)
	}
	cancel()
	if err != nil {
		return nil, atStage("proxy-tls", err)
	}
	return tlsConn, nil
}

// SOCKS owns negotiation and cleanup; its upstream socket uses the same native
// dial hook and connection wrappers as every other route.
type proxyDialFunc func(context.Context, string, string) (net.Conn, error)

func (f proxyDialFunc) Dial(network, address string) (net.Conn, error) {
	return f(context.Background(), network, address)
}

func (f proxyDialFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return f(ctx, network, address)
}

// LookupResult retains whether both address families completed their lookup.
type LookupResult struct {
	Addresses []net.IP
	Complete  bool
}

// Lookup uses system DNS for address safety checks. Ordinary HTTP requests
// retain the native transport's DNS behavior, including proxy-side resolution.
func Lookup(ctx context.Context, host string) (LookupResult, error) {
	if err := ctx.Err(); err != nil {
		return LookupResult{}, err
	}
	if loopbackHost(host) {
		return LookupResult{Addresses: []net.IP{net.IPv6loopback, net.IPv4(127, 0, 0, 1)}, Complete: true}, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return LookupResult{Addresses: []net.IP{ip}, Complete: true}, nil
	}
	if _, err := FromContext(ctx); err != nil {
		return LookupResult{}, err
	}
	host, err := addressHost(host)
	if err != nil {
		return LookupResult{}, err
	}
	// A combined LookupIP("ip") can discard one family's error when the other
	// succeeds. Keep the native per-family results to classify that case safely.
	resolver := net.DefaultResolver
	var addresses [2][]net.IP
	var failures [2]error
	var pending sync.WaitGroup
	for i, family := range []string{"ip4", "ip6"} {
		pending.Go(func() {
			addresses[i], failures[i] = resolver.LookupIP(ctx, family, host)
		})
	}
	pending.Wait()
	if err := ctx.Err(); err != nil {
		return LookupResult{}, err
	}
	result := LookupResult{Complete: true}
	for i, failure := range failures {
		result.Addresses = append(result.Addresses, addresses[i]...)
		var dns *net.DNSError
		if failure != nil && !(errors.As(failure, &dns) && dns.IsNotFound) {
			result.Complete = false
		}
	}
	if len(result.Addresses) == 0 {
		if err := errors.Join(failures[:]...); err != nil {
			return result, err
		}
		return result, errors.New("DNS lookup returned no addresses")
	}
	return result, nil
}
func IsLocalName(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host == "localhost" || !strings.Contains(host, ".") || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal")
}
