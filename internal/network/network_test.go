package network

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vista/internal/configstore"
)

func networkFixtureCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"origin.test", "entry.test", "xn--bcher-kva.test"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return cert, roots
}
func networkFixtureTLSServer(t *testing.T, cert tls.Certificate, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(handler)
	s.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

type networkFixtureProxy struct {
	profile Profile
	mu      sync.Mutex
	targets []string
}

func networkFixtureStartProxy(t *testing.T, kind string, cert tls.Certificate) *networkFixtureProxy {
	t.Helper()
	p := &networkFixtureProxy{}
	record := func(address string) { p.mu.Lock(); p.targets = append(p.targets, address); p.mu.Unlock() }
	connect := func(address string) (net.Conn, error) {
		record(address)
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if host == "origin.test" || host == "resolver.test" {
			host = "127.0.0.1"
		}
		return net.DialTimeout("tcp", net.JoinHostPort(host, port), 2*time.Second)
	}
	var address string
	if kind == "socks5" {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { listener.Close() })
		address = listener.Addr().String()
		go func() {
			for {
				c, err := listener.Accept()
				if err != nil {
					return
				}
				go func(c net.Conn) {
					defer c.Close()
					c.SetDeadline(time.Now().Add(10 * time.Second))
					var head [2]byte
					if _, err := io.ReadFull(c, head[:]); err != nil {
						return
					}
					methods := make([]byte, int(head[1]))
					if _, err := io.ReadFull(c, methods); err != nil {
						return
					}
					c.Write([]byte{5, 0})
					var req [4]byte
					if _, err := io.ReadFull(c, req[:]); err != nil {
						return
					}
					var host string
					switch req[3] {
					case 1:
						var ip [4]byte
						if _, err := io.ReadFull(c, ip[:]); err != nil {
							return
						}
						host = net.IP(ip[:]).String()
					case 3:
						var size [1]byte
						c.Read(size[:])
						name := make([]byte, int(size[0]))
						io.ReadFull(c, name)
						host = string(name)
					case 4:
						var ip [16]byte
						io.ReadFull(c, ip[:])
						host = net.IP(ip[:]).String()
					default:
						return
					}
					var port [2]byte
					io.ReadFull(c, port[:])
					remote, err := connect(net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:])))))
					if err != nil {
						return
					}
					defer remote.Close()
					c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
					c.SetDeadline(time.Time{})
					go io.Copy(remote, c)
					io.Copy(c, remote)
				}(c)
			}
		}()
	} else {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodConnect {
				remote, err := connect(r.Host)
				if err != nil {
					http.Error(w, "connect", 502)
					return
				}
				defer remote.Close()
				c, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					return
				}
				defer c.Close()
				io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
				go io.Copy(remote, c)
				io.Copy(c, remote)
				return
			}
			record(r.URL.Host)
			out := r.Clone(r.Context())
			u := *r.URL
			out.URL = &u
			if u.Hostname() == "origin.test" || u.Hostname() == "resolver.test" {
				u.Host = net.JoinHostPort("127.0.0.1", u.Port())
			}
			out.RequestURI = ""
			out.Header.Del("Proxy-Authorization")
			response, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(out)
			if err != nil {
				http.Error(w, "forward", 502)
				return
			}
			defer response.Body.Close()
			for name, values := range response.Header {
				for _, value := range values {
					w.Header().Add(name, value)
				}
			}
			w.WriteHeader(response.StatusCode)
			io.Copy(w, response.Body)
		})
		var server *httptest.Server
		if kind == "https" {
			server = networkFixtureTLSServer(t, cert, handler)
		} else {
			server = httptest.NewServer(handler)
			t.Cleanup(server.Close)
		}
		u, _ := url.Parse(server.URL)
		address = u.Host
	}
	_, port, _ := net.SplitHostPort(address)
	n, _ := strconv.Atoi(port)
	p.profile = Profile{ID: kind, Name: kind, Protocol: kind, Server: "entry.test", ConnectIP: "127.0.0.1", Port: n, Authentication: "none"}
	return p
}
func TestExplicitRoutingAndAddressPinning(t *testing.T) {
	cert, roots := networkFixtureCertificate(t)
	var mu sync.Mutex
	var hosts []string
	var names []string
	origin := networkFixtureTLSServer(t, cert, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hosts = append(hosts, r.Host)
		names = append(names, r.TLS.ServerName)
		mu.Unlock()
		io.WriteString(w, "ok")
	})
	originURL, _ := url.Parse(origin.URL)
	originURL.Host = net.JoinHostPort("origin.test", originURL.Port())
	bad := (&url.URL{Scheme: "http", Host: "127.0.0.1:1"}).String()
	for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(name, bad)
	}
	t.Setenv("NO_PROXY", "*")
	t.Setenv("no_proxy", "*")
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	for _, kind := range []string{"direct", "http", "https", "socks5"} {
		t.Run(kind, func(t *testing.T) {
			var nativeReads atomic.Int32
			native := base.Clone()
			native.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				conn, err := base.DialContext(ctx, network, address)
				if err != nil {
					return nil, err
				}
				return nativeReadObservedConn{Conn: conn, reads: &nativeReads}, nil
			}
			policy := Policy{}
			var proxy *networkFixtureProxy
			if kind != "direct" {
				proxy = networkFixtureStartProxy(t, kind, cert)
				policy.Profile = &proxy.profile
			}
			client := &http.Client{Transport: Transport{Base: native, Policy: &policy}, Timeout: 10 * time.Second}
			defer client.CloseIdleConnections()
			ctx, err := WithAddresses(t.Context(), "origin.test", []net.IP{net.ParseIP("127.0.0.1")})
			if err != nil {
				t.Fatal(err)
			}
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, originURL.String(), nil)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if nativeReads.Load() == 0 {
				t.Fatal("bound route discarded the native dialer's connection wrapper")
			}
			if proxy != nil {
				proxy.mu.Lock()
				defer proxy.mu.Unlock()
				if len(proxy.targets) != 1 {
					t.Fatalf("expected one pinned origin connection, got %d", len(proxy.targets))
				}
				for _, target := range proxy.targets {
					host, _, _ := net.SplitHostPort(target)
					if host != "127.0.0.1" {
						t.Fatalf("proxy received unresolved hostname %q", host)
					}
				}
			}
		})
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hosts) != 4 {
		t.Fatal(hosts)
	}
	for i, host := range hosts {
		if host != originURL.Host || names[i] != "origin.test" {
			t.Fatalf("identity changed: %q %q", host, names[i])
		}
	}
}
func TestPrivateCredentialCodec(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	profile := Profile{ID: "profile", Name: "Profile", Protocol: "http", Server: "entry.test", Port: 80, Authentication: "password", Username: "temporary-user", Password: "temporary-secret"}
	settings := Settings{Profiles: []Profile{profile}, GlobalProxy: profile.ID}
	data, _ := json.Marshal(settings)
	if err := configstore.Write(dir, configstore.Network, data); err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(public), profile.Password) || strings.Contains(string(public), profile.Username) {
		t.Fatal("credentials in public config")
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Profiles[0].Password != profile.Password {
		t.Fatal("credential round trip failed")
	}
}
func TestBoundHTTPPinsAuthorityAndHost(t *testing.T) {
	cert, roots := networkFixtureCertificate(t)
	var observedHost string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { observedHost = r.Host; io.WriteString(w, "ok") }))
	defer origin.Close()
	p := networkFixtureStartProxy(t, "http", cert)
	policy := Policy{Profile: &p.profile}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{RootCAs: roots}
	u, _ := url.Parse(origin.URL)
	u.Host = net.JoinHostPort("origin.test", u.Port())
	ctx, err := WithAddresses(t.Context(), "origin.test", []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	client := &http.Client{Transport: Transport{Base: base, Policy: &policy}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || observedHost != u.Host {
		t.Fatalf("origin identity changed: status=%d host=%q", response.StatusCode, observedHost)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, target := range p.targets {
		host, _, _ := net.SplitHostPort(target)
		if host != "127.0.0.1" {
			t.Fatalf("proxy received an unvalidated hostname: %q", host)
		}
	}
}

func TestRequestSnapshotSelectsConnectionsConsistently(t *testing.T) {
	a := Profile{ID: "a", Name: "A", Protocol: "http", Server: "proxy.example", Port: 8080, Authentication: "none"}
	b := a
	b.ID = "b"
	b.Name = "B"
	settings := Settings{GlobalProxy: a.ID, Profiles: []Profile{a, b}}
	global, err := settings.Select(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithPolicy(WithSettings(context.Background(), settings), global)
	selected, err := ForConnection(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := FromContext(selected)
	if err != nil || policy.Profile == nil || policy.Profile.ID != b.ID || policy.Profile.Server != b.Server {
		t.Fatal("connection did not use the request snapshot")
	}
	policy.Profile.Server = "pinned.example"
	pinned, err := ForConnection(WithConnectionPolicy(ctx, policy), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	retained, _ := FromContext(pinned)
	if retained.Profile.Server != policy.Profile.Server {
		t.Fatal("nested call replaced an explicit execution snapshot")
	}
}

func TestAddressBindingsNormalizeHostIdentities(t *testing.T) {
	for _, check := range []struct{ stored, dialed, canonical string }{
		{"bücher.test", "xn--bcher-kva.test", "xn--bcher-kva.test"},
		{"xn--bcher-kva.test", "bücher.test", "xn--bcher-kva.test"},
		{"BÜCHER.TEST.", "xn--bcher-kva.test", "xn--bcher-kva.test"},
		{"bücher.test。", "XN--BCHER-KVA.TEST.", "xn--bcher-kva.test"},
		{"ASCII.TEST.", "ascii.test", "ascii.test"},
		{"192.0.2.1", "192.0.2.1", "192.0.2.1"},
		{"2001:DB8:0:0::1", "2001:db8::1", "2001:db8::1"},
		{"::ffff:c000:201", "::ffff:192.0.2.1", "::ffff:192.0.2.1"},
		{"fe80::1%fixture", "fe80:0:0::1%fixture", "fe80::1%fixture"},
	} {
		t.Run(check.stored, func(t *testing.T) {
			ctx, err := WithAddresses(context.Background(), check.stored, []net.IP{net.ParseIP("192.0.2.1")})
			if err != nil {
				t.Fatal(err)
			}
			if got := ctx.Value(addressKey{}).(addressSet).host; got != check.canonical {
				t.Fatalf("binding key = %q, want %q", got, check.canonical)
			}
			ips, bound, err := pinnedAddresses(ctx, check.dialed)
			if err != nil || !bound || !reflect.DeepEqual(ips, []string{"192.0.2.1"}) {
				t.Fatalf("binding lookup = %v, %v, %v", ips, bound, err)
			}
			if _, bound, err := pinnedAddresses(ctx, "unrelated.test"); err != nil || bound {
				t.Fatalf("binding applied to another host: bound=%v error=%v", bound, err)
			}
		})
	}
}

func TestAddressBindingErrorsStopBeforeDial(t *testing.T) {
	ips := []net.IP{net.ParseIP("192.0.2.1")}
	ctx, err := WithAddresses(WithPolicy(context.Background(), Policy{}), "valid.test", ips)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"", ".", "bad host.test", "bad\x00.test"} {
		if _, err := WithAddresses(context.Background(), host, ips); err == nil {
			t.Errorf("invalid binding host accepted: %q", host)
		}
		if _, _, err := pinnedAddresses(ctx, host); err == nil {
			t.Errorf("invalid lookup host accepted: %q", host)
		}
	}
	if _, err := WithAddresses(context.Background(), "valid.test", []net.IP{nil}); err == nil {
		t.Fatal("empty address binding accepted")
	}
	base := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		t.Error("invalid binding lookup reached the dialer")
		return nil, errors.New("unexpected dial")
	}}
	request := &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "http", Host: "bad host.test"}, Header: make(http.Header)}
	if _, err := (Transport{Base: base}).RoundTrip(request.WithContext(ctx)); err == nil || !strings.Contains(err.Error(), "invalid address binding hostname") {
		t.Fatalf("transport did not propagate binding failure: %v", err)
	}
	e := &engine{base: base}
	if _, err := e.dial(ctx, "tcp", "bad host.test:80"); err == nil || !strings.Contains(err.Error(), "invalid address binding hostname") {
		t.Fatalf("dial did not propagate binding failure: %v", err)
	}
}

func TestIDNAAddressBindingPreservesHTTPAndTLSIdentity(t *testing.T) {
	cert, roots := networkFixtureCertificate(t)
	observed := make(chan [2]string, 1)
	origin := networkFixtureTLSServer(t, cert, func(w http.ResponseWriter, r *http.Request) {
		observed <- [2]string{r.Host, r.TLS.ServerName}
		io.WriteString(w, "ok")
	})
	u, _ := url.Parse(origin.URL)
	port := u.Port()
	u.Host = net.JoinHostPort("bücher.test", port)
	ctx, err := WithAddresses(WithPolicy(context.Background(), Policy{}), "bücher.test", []net.IP{net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	base := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, DialContext: func(ctx context.Context, kind, address string) (net.Conn, error) {
		if address != origin.Listener.Addr().String() {
			return nil, fmt.Errorf("dial bypassed bound loopback address: %q", address)
		}
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, kind, address)
	}}
	defer (Transport{Base: base}).CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := Do(&http.Client{Transport: base, Timeout: 5 * time.Second}, request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "ok" {
		t.Fatalf("response = %q, error=%v", body, err)
	}
	if got, want := <-observed, [2]string{net.JoinHostPort("xn--bcher-kva.test", port), "xn--bcher-kva.test"}; got != want {
		t.Fatalf("HTTP/TLS identity = %v, want %v", got, want)
	}
	if request.URL.Hostname() != "bücher.test" {
		t.Fatal("binding normalization rewrote the request URL")
	}
}
