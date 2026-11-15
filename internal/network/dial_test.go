package network

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolvedAddressesFallbackAndLateCleanup(t *testing.T) {
	for _, winner := range []string{"192.0.2.2", "2001:db8::2"} {
		t.Run(winner, func(t *testing.T) {
			lateClosed := make(chan struct{})
			var mu sync.Mutex
			var attempts []string
			base := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				host, _, _ := net.SplitHostPort(address)
				mu.Lock()
				attempts = append(attempts, host)
				mu.Unlock()
				client, server := net.Pipe()
				server.Close()
				if host == winner {
					return client, nil
				}
				<-ctx.Done()
				// Simulate a successful connection arriving after another wins.
				return &fallbackTrackedConn{Conn: client, closed: lateClosed}, nil
			}}
			e := &engine{base: base, policy: Policy{}}
			ctx, err := WithAddresses(context.Background(), "fixed.invalid", []net.IP{net.ParseIP("2001:db8::1"), net.ParseIP(winner)})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			conn, err := e.dial(ctx, "tcp", "fixed.invalid:443")
			if err != nil {
				t.Fatalf("available address was blocked: %v", err)
			}
			conn.Close()
			select {
			case <-lateClosed:
			case <-time.After(time.Second):
				t.Fatal("losing connection was not cancelled and closed")
			}
			mu.Lock()
			defer mu.Unlock()
			if len(attempts) != 2 || !slices.Contains(attempts, winner) || !slices.Contains(attempts, "2001:db8::1") {
				t.Fatalf("dialed outside the supplied addresses or skipped fallback: %v", attempts)
			}
		})
	}
}

func TestBoundMultipleAddressesShareBudget(t *testing.T) {
	base := &http.Transport{DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(address)
		if host == "192.0.2.1" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if host != "192.0.2.2" {
			return nil, errors.New("dial escaped the validated binding")
		}
		client, server := net.Pipe()
		server.Close()
		return client, nil
	}}
	e := &engine{base: base}
	ctx, err := WithAddresses(context.Background(), "fixed.invalid", []net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.2")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := e.dial(ctx, "tcp", "fixed.invalid:443")
	if err != nil {
		t.Fatalf("first bound address consumed the entire budget: %v", err)
	}
	conn.Close()
}

func TestResolvedAddressesPreserveProxyAndTLS(t *testing.T) {
	cert, roots := networkFixtureCertificate(t)
	origin := networkFixtureTLSServer(t, cert, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fixed" || r.TLS.ServerName != "origin.test" || !strings.HasPrefix(r.Host, "origin.test:") {
			t.Errorf("HTTP/TLS identity changed: host=%s serverName=%s", r.Host, r.TLS.ServerName)
		}
		io.WriteString(w, "fixed-result")
	})
	u, _ := url.Parse(origin.URL)
	u.Host = net.JoinHostPort("origin.test", u.Port())
	u.Path = "/fixed"
	for _, kind := range []string{"direct", "http", "https", "socks5"} {
		t.Run(kind, func(t *testing.T) {
			policy := Policy{}
			var proxy *networkFixtureProxy
			if kind != "direct" {
				proxy = networkFixtureStartProxy(t, kind, cert)
				policy.Profile = &proxy.profile
			}
			var nativeReads atomic.Int32
			base := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				conn, err := new(net.Dialer).DialContext(ctx, network, address)
				if err != nil {
					return nil, err
				}
				return nativeReadObservedConn{Conn: conn, reads: &nativeReads}, nil
			}}
			client := &http.Client{Transport: Transport{Base: base, Policy: &policy}, Timeout: 5 * time.Second}
			defer client.CloseIdleConnections()
			ctx, err := WithAddresses(context.Background(), "origin.test", []net.IP{net.ParseIP("127.0.0.2"), net.ParseIP("127.0.0.1")})
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				request, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || string(body) != "fixed-result" {
					t.Fatalf("bad fixed-address response: %q %v", body, err)
				}
			}
			if nativeReads.Load() == 0 {
				t.Fatal("selected route discarded the native dialer's connection wrapper")
			}
			if proxy != nil {
				proxy.mu.Lock()
				defer proxy.mu.Unlock()
				if len(proxy.targets) != 2 {
					t.Fatalf("fallback/reuse did not use the selected proxy: %v", proxy.targets)
				}
				for _, address := range proxy.targets {
					host, _, _ := net.SplitHostPort(address)
					if host != "127.0.0.1" && host != "127.0.0.2" {
						t.Fatalf("proxy received an unvalidated target: %s", address)
					}
				}
			}
		})
	}
}

type nativeReadObservedConn struct {
	net.Conn
	reads *atomic.Int32
}

func (c nativeReadObservedConn) Read(p []byte) (int, error) {
	c.reads.Add(1)
	return c.Conn.Read(p)
}

func TestHTTPSProxyHandshakeBudgetAndCleanup(t *testing.T) {
	cert, roots := networkFixtureCertificate(t)
	for _, name := range []string{"timeout", "parent-deadline", "cancel", "disabled", "success", "native-timeout", "native-parent-deadline", "native-cancel", "native-disabled", "native-success"} {
		t.Run(name, func(t *testing.T) {
			native := strings.HasPrefix(name, "native-")
			mode := strings.TrimPrefix(name, "native-")
			budget, parentBudget := 200*time.Millisecond, 2*time.Second
			if mode == "disabled" {
				budget = 0
			} else if mode == "parent-deadline" || mode == "cancel" {
				budget = time.Second
				if mode == "parent-deadline" {
					parentBudget = 50 * time.Millisecond
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), parentBudget)
			defer cancel()
			client, peer := net.Pipe()
			defer peer.Close()
			closed := make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			wantFailure := mode != "disabled" && mode != "success"
			go func() {
				defer peer.Close()
				server := tls.Server(peer, &tls.Config{Certificates: []tls.Certificate{cert}, GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
					if mode == "cancel" {
						cancel()
					}
					if wantFailure {
						<-release
					} else if mode == "disabled" {
						// A zero budget must allow a handshake beyond the nonzero case.
						select {
						case <-time.After(300 * time.Millisecond):
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					}
					return nil, nil
				}})
				// The peer has its own lifetime. Sharing the client's cancellation
				// also closes the peer and races EOF against the client context.
				if err := server.Handshake(); err != nil {
					return
				}
				if !native {
					if _, err := http.ReadRequest(bufio.NewReader(server)); err != nil {
						return
					}
					if _, err := io.WriteString(server, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
						return
					}
				}
				// Later traffic must survive expiration of the handshake budget.
				select {
				case <-time.After(400 * time.Millisecond):
					server.Write([]byte("x"))
				case <-ctx.Done():
				}
			}()
			base := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, TLSHandshakeTimeout: budget,
				DialContext: func(context.Context, string, string) (net.Conn, error) {
					return &handshakeTrackedConn{Conn: client, closed: closed}, nil
				}}
			e := &engine{base: base, policy: Policy{Profile: &Profile{Protocol: "https", Server: "entry.test", Port: 443, InsecureSkipVerify: native}}}
			started := time.Now()
			var conn net.Conn
			var err error
			if native {
				conn, err = engineFor(e.policy, base).transport.DialTLSContext(ctx, "tcp", "entry.test:443")
			} else {
				conn, err = e.through(ctx, "tcp", "origin.test:80")
			}
			if wantFailure {
				matches := errors.Is(err, context.DeadlineExceeded)
				if mode == "cancel" {
					// This tests the dial helper below net/http's cancellation-error
					// normalization. Its socket-close callback can wake TLS before
					// cancellation reaches TLS's child context.
					matches = ctx.Err() == context.Canceled && time.Since(started) < budget &&
						(errors.Is(err, context.Canceled) || errors.Is(err, io.ErrClosedPipe))
				} else if mode == "parent-deadline" {
					// The socket deadline and context timer share the parent deadline;
					// either may interrupt TLS first, as in the native transport.
					var timeout net.Error
					matches = errors.As(err, &timeout) && timeout.Timeout() && time.Since(started) < budget
				}
				if conn != nil || !matches || FailureStage(err) != "proxy-tls" || (mode == "timeout" && ctx.Err() != nil) {
					t.Fatalf("TLS/parent budget lost: conn=%v error=%v parent=%v", conn, err, ctx.Err())
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				var data [1]byte
				_, err := io.ReadFull(conn, data[:])
				conn.Close()
				if err != nil || data[0] != 'x' {
					t.Fatalf("completed handshake retained its deadline: %q %v", data, err)
				}
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("HTTPS proxy socket remained open")
			}
		})
	}
}

type handshakeTrackedConn struct {
	net.Conn
	closed chan struct{}
	once   sync.Once
}

func (c *handshakeTrackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.closed) })
	return err
}

type fallbackTrackedConn struct {
	net.Conn
	closed chan struct{}
}

func (c *fallbackTrackedConn) Close() error {
	err := c.Conn.Close()
	close(c.closed)
	return err
}

func TestResolvedAddressesCancellationAndLiteralBinding(t *testing.T) {
	for _, authority := range []string{"fixed.invalid", "192.0.2.99"} {
		t.Run(authority, func(t *testing.T) {
			stopped := make(chan struct{}, 2)
			started := make(chan struct{}, 2)
			e := &engine{base: &http.Transport{DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
				host, _, _ := net.SplitHostPort(address)
				if host != "192.0.2.1" && host != "192.0.2.2" {
					t.Errorf("binding bypassed: %s", address)
				}
				started <- struct{}{}
				<-ctx.Done()
				stopped <- struct{}{}
				return nil, ctx.Err()
			}}}
			ctx, err := WithAddresses(context.Background(), authority, []net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.2")})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			go func() {
				for range 2 {
					select {
					case <-started:
					case <-ctx.Done():
						return
					}
				}
				cancel()
			}()
			if conn, err := e.dial(ctx, "tcp", net.JoinHostPort(authority, "443")); conn != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: conn=%v err=%v", conn, err)
			}
			for range 2 {
				select {
				case <-stopped:
				case <-time.After(time.Second):
					t.Fatal("an address attempt did not stop")
				}
			}
		})
	}
}

func TestResolvedAddressesRespectDeadline(t *testing.T) {
	e := &engine{base: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}}
	ctx, err := WithAddresses(context.Background(), "fixed.invalid", []net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.2")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if conn, err := e.dial(ctx, "tcp", "fixed.invalid:443"); conn != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline lost: conn=%v err=%v", conn, err)
	}
}
