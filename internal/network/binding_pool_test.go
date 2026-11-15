package network

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type poolTrackedConn struct {
	net.Conn
	closed *atomic.Int32
}

func (c *poolTrackedConn) Close() error {
	c.closed.Add(1)
	return c.Conn.Close()
}

func TestCloseIdleConnectionsOnlyClosesOwnedPools(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "pool")
	}))
	defer upstream.Close()
	var opened, closed [2]atomic.Int32
	var clients [2]*http.Client
	for index := range clients {
		base := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := new(net.Dialer).DialContext(ctx, network, upstream.Listener.Addr().String())
			if err != nil {
				return nil, err
			}
			opened[index].Add(1)
			return &poolTrackedConn{conn, &closed[index]}, nil
		}}
		clients[index] = &http.Client{Transport: Transport{Base: base, Policy: &Policy{}}}
		defer clients[index].CloseIdleConnections()
	}
	read := func(client *http.Client, ctx context.Context) {
		t.Helper()
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://origin.test/", nil)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}
	binding, err := WithAddresses(context.Background(), "origin.test", []net.IP{net.ParseIP("192.0.2.1")})
	if err != nil {
		t.Fatal(err)
	}
	for _, client := range clients {
		read(client, context.Background())
		read(client, binding)
	}
	clients[0].CloseIdleConnections()
	if opened[0].Load() != 2 || closed[0].Load() != 2 || closed[1].Load() != 0 {
		t.Fatalf("pool close affected the wrong owner: opened=%d/%d closed=%d/%d", opened[0].Load(), opened[1].Load(), closed[0].Load(), closed[1].Load())
	}
	read(clients[1], context.Background())
	read(clients[1], binding)
	if opened[1].Load() != 2 {
		t.Fatal("unrelated client lost connection reuse")
	}
}

func TestAddressBindingPoolIsolationAndReuse(t *testing.T) {
	for _, protocol := range []string{"http", "https", "h2"} {
		t.Run(protocol, func(t *testing.T) {
			cert, roots := networkFixtureCertificate(t)
			start := func(body string) *httptest.Server {
				s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Host != "origin.test" || r.TLS != nil && r.TLS.ServerName != "origin.test" {
						t.Errorf("authority changed: host=%s TLS=%+v", r.Host, r.TLS)
					}
					fmt.Fprint(w, body)
				}))
				if protocol == "http" {
					s.Start()
				} else {
					s.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
					s.EnableHTTP2 = protocol == "h2"
					s.StartTLS()
				}
				t.Cleanup(s.Close)
				return s
			}
			a, b := start("A"), start("B")
			var mu sync.Mutex
			var dials []string
			base := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, ForceAttemptHTTP2: protocol == "h2"}
			base.DialContext = func(ctx context.Context, kind, address string) (net.Conn, error) {
				mu.Lock()
				dials = append(dials, address)
				mu.Unlock()
				host, _, _ := net.SplitHostPort(address)
				target := a.Listener.Addr().String()
				if host == "192.0.2.2" {
					target = b.Listener.Addr().String()
				} else if host != "192.0.2.1" {
					return nil, fmt.Errorf("unexpected dial: %s", address)
				}
				return new(net.Dialer).DialContext(ctx, kind, target)
			}
			client := &http.Client{Transport: Transport{Base: base, Policy: &Policy{}}, Timeout: 3 * time.Second}
			defer client.CloseIdleConnections()
			scheme := "https"
			if protocol == "http" {
				scheme = "http"
			}
			for _, step := range []struct{ ip, want string }{{"192.0.2.1", "A"}, {"192.0.2.1", "A"}, {"192.0.2.2", "B"}, {"192.0.2.1", "A"}} {
				ctx, err := WithAddresses(context.Background(), "ORIGIN.test.", []net.IP{net.ParseIP(step.ip)})
				if err != nil {
					t.Fatal(err)
				}
				req, _ := http.NewRequestWithContext(ctx, "GET", scheme+"://origin.test/page", nil)
				res, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(res.Body)
				res.Body.Close()
				if err != nil || string(data) != step.want {
					t.Fatalf("binding %s: body=%q want=%q error=%v", step.ip, data, step.want, err)
				}
				if protocol == "h2" && res.ProtoMajor != 2 {
					t.Fatalf("HTTP/2 coverage did not execute: %s", res.Proto)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(dials) != 2 {
				t.Fatalf("same binding did not reuse its connection: %v", dials)
			}
		})
	}
}

func TestChangedBindingCannotBorrowConnectionWhileDialing(t *testing.T) {
	enteredA, releaseA := make(chan struct{}), make(chan struct{})
	enteredB, releaseB := make(chan struct{}), make(chan struct{})
	var onceA, onceB sync.Once
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-enteredA:
		default:
			close(enteredA)
		}
		<-releaseA
		fmt.Fprint(w, "A")
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "B") }))
	defer b.Close()
	defer onceA.Do(func() { close(releaseA) })
	defer onceB.Do(func() { close(releaseB) })
	base := &http.Transport{DialContext: func(ctx context.Context, kind, address string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(address)
		target := a.Listener.Addr().String()
		if host == "192.0.2.2" {
			close(enteredB)
			select {
			case <-releaseB:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			target = b.Listener.Addr().String()
		} else if host != "192.0.2.1" {
			return nil, fmt.Errorf("unexpected dial: %s", address)
		}
		return new(net.Dialer).DialContext(ctx, kind, target)
	}}
	client := &http.Client{Transport: Transport{Base: base, Policy: &Policy{}}, Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	start := func(ip string) <-chan string {
		result := make(chan string, 1)
		go func() {
			ctx, err := WithAddresses(context.Background(), "origin.test", []net.IP{net.ParseIP(ip)})
			if err != nil {
				result <- err.Error()
				return
			}
			req, _ := http.NewRequestWithContext(ctx, "GET", "http://origin.test/", nil)
			response, err := client.Do(req)
			if err != nil {
				result <- err.Error()
				return
			}
			data, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				result <- err.Error()
				return
			}
			result <- string(data)
		}()
		return result
	}
	wait := func(signal <-chan struct{}) {
		t.Helper()
		select {
		case <-signal:
		case <-time.After(3 * time.Second):
			t.Fatal("request did not reach the synchronization point")
		}
	}
	first := start("192.0.2.1")
	wait(enteredA)
	second := start("192.0.2.2")
	wait(enteredB)
	onceA.Do(func() { close(releaseA) })
	if got := <-first; got != "A" {
		t.Fatalf("in-flight original binding was interrupted: %q", got)
	}
	// A has become idle while B's dial is still outstanding. It must not be
	// handed to B's waiter, even though their HTTP authority is identical.
	onceB.Do(func() { close(releaseB) })
	if got := <-second; got != "B" {
		t.Fatalf("changed binding borrowed another pool's connection: %q", got)
	}
}

func TestAddressBindingSetOrderRetainsReuse(t *testing.T) {
	var dials atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "ok") }))
	defer s.Close()
	base := &http.Transport{DialContext: func(ctx context.Context, kind, address string) (net.Conn, error) {
		dials.Add(1)
		return new(net.Dialer).DialContext(ctx, kind, s.Listener.Addr().String())
	}}
	client := &http.Client{Transport: Transport{Base: base, Policy: &Policy{}}, Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	for _, values := range [][]string{{"192.0.2.1", "192.0.2.2"}, {"192.0.2.2", "192.0.2.1", "192.0.2.1"}} {
		var ips []net.IP
		for _, value := range values {
			ips = append(ips, net.ParseIP(value))
		}
		ctx, _ := WithAddresses(context.Background(), "origin.test", ips)
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://origin.test/", nil)
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}
	if dials.Load() != 1 {
		t.Fatalf("reordering the same validated set opened %d connections", dials.Load())
	}
}

func TestBoundPoolEvictionKeepsActiveRequest(t *testing.T) {
	entered, release, closed := make(chan struct{}), make(chan struct{}), make(chan struct{}, 2)
	var once sync.Once
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		fmt.Fprint(w, "complete")
	}))
	s.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			closed <- struct{}{}
		}
	}
	s.Start()
	defer s.Close()
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	base := &http.Transport{DialContext: func(ctx context.Context, kind, _ string) (net.Conn, error) {
		return new(net.Dialer).DialContext(ctx, kind, s.Listener.Addr().String())
	}}
	client := &http.Client{Transport: Transport{Base: base, Policy: &Policy{}}, Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	result := make(chan string, 1)
	go func() {
		ctx, _ := WithAddresses(context.Background(), "origin.test", []net.IP{net.ParseIP("192.0.2.1")})
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://origin.test/", nil)
		res, err := client.Do(req)
		if err != nil {
			result <- err.Error()
			return
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			result <- err.Error()
			return
		}
		result <- string(body)
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("active request did not start")
	}
	e := engineFor(Policy{}, base)
	for i := 1; i <= 65; i++ {
		e.boundTransport(addressSet{host: "origin.test", addresses: []string{fmt.Sprintf("203.0.113.%d", i)}})
	}
	boundTransports.Lock()
	count := len(boundTransports.values)
	boundTransports.Unlock()
	if count > 64 {
		t.Fatalf("binding pools exceeded their global bound: %d", count)
	}
	releaseOnce.Do(func() { close(release) })
	if got := <-result; got != "complete" {
		t.Fatalf("eviction interrupted the active response: %q", got)
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("evicted connection was retained after its response completed")
	}
}

func TestLocalhostSupportsBothLoopbackFamilies(t *testing.T) {
	for _, family := range []string{"tcp4", "tcp6", "tcp"} {
		t.Run(family, func(t *testing.T) {
			address := "127.0.0.1:0"
			if family == "tcp6" {
				address = "[::1]:0"
			} else if family == "tcp" {
				address = "[::]:0"
			}
			listener, err := net.Listen(family, address)
			if err != nil {
				t.Fatalf("required loopback listener unavailable: %v", err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, family) })}
			go server.Serve(listener)
			defer server.Close()
			port := listener.Addr().(*net.TCPAddr).Port
			p := Policy{Profile: &Profile{ID: "unused", Name: "Unused", Protocol: "http", Server: "127.0.0.1", Port: 9, Authentication: "none"}}
			client := &http.Client{Transport: Transport{Policy: &p}, Timeout: 3 * time.Second}
			defer client.CloseIdleConnections()
			hosts := []string{"localhost", "service.localhost", listener.Addr().(*net.TCPAddr).IP.String()}
			if family == "tcp" {
				hosts = []string{"localhost", "service.localhost", "::1", "127.0.0.1"}
			}
			for _, host := range hosts {
				u := fmt.Sprintf("http://%s/", net.JoinHostPort(host, fmt.Sprint(port)))
				res, err := client.Get(u)
				if err != nil {
					t.Fatalf("%s: %v", u, err)
				}
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
			}
			lookup, err := Lookup(WithPolicy(context.Background(), p), "LOCALHOST.")
			var values []string
			for _, ip := range lookup.Addresses {
				values = append(values, ip.String())
			}
			if err != nil || !lookup.Complete || !slices.Contains(values, "::1") || !slices.Contains(values, "127.0.0.1") {
				t.Fatalf("lookup must validate both loopback families: %v %v", values, err)
			}
		})
	}
}
