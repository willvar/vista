package network

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProxyTLSVerificationScopes(t *testing.T) {
	originCert, originRoots := networkFixtureCertificate(t)
	proxyCert, _ := networkFixtureCertificate(t)
	proxyLeaf, err := x509.ParseCertificate(proxyCert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	proxy := networkFixtureStartProxy(t, "https", proxyCert)
	for _, targetTLS := range []bool{false, true} {
		for _, bound := range []bool{false, true} {
			for _, tc := range []struct {
				name                                       string
				skip, trustProxy, trustOrigin, sdkInsecure bool
				wantError                                  bool
			}{
				{name: "verified-default", trustOrigin: true, wantError: true},
				{name: "proxy-opt-out", skip: true, trustOrigin: true},
				{name: "trusted-proxy", trustProxy: true, trustOrigin: true},
				{name: "destination-stays-verified", skip: true, trustProxy: true, wantError: targetTLS},
				{name: "proxy-stays-verified-with-sdk-opt-out", sdkInsecure: true, wantError: true},
			} {
				name := tc.name + "/http"
				if targetTLS {
					name = tc.name + "/https"
				}
				if bound {
					name += "/bound"
				} else {
					name += "/ordinary"
				}
				t.Run(name, func(t *testing.T) {
					origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if targetTLS && r.TLS.ServerName != "origin.test" {
							t.Error("destination SNI lost")
						}
						io.WriteString(w, "ok")
					}))
					if targetTLS {
						origin.EnableHTTP2 = true
						origin.TLS = &tls.Config{Certificates: []tls.Certificate{originCert}}
						origin.StartTLS()
					} else {
						origin.Start()
					}
					defer origin.Close()
					u, _ := url.Parse(origin.URL)
					u.Host = net.JoinHostPort("origin.test", u.Port())
					roots := x509.NewCertPool()
					if tc.trustOrigin {
						roots = originRoots.Clone()
					}
					if tc.trustProxy {
						roots.AddCert(proxyLeaf)
					}
					base := http.DefaultTransport.(*http.Transport).Clone()
					base.TLSClientConfig = &tls.Config{RootCAs: roots, InsecureSkipVerify: tc.sdkInsecure}
					var mu sync.Mutex
					var sockets []*handshakeTrackedConn
					var dials atomic.Int32
					nativeDial := base.DialContext
					base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
						raw, err := nativeDial(ctx, network, address)
						if err != nil {
							return nil, err
						}
						dials.Add(1)
						conn := &handshakeTrackedConn{Conn: raw, closed: make(chan struct{})}
						mu.Lock()
						sockets = append(sockets, conn)
						mu.Unlock()
						return conn, nil
					}
					profile := proxy.profile
					profile.InsecureSkipVerify = tc.skip
					policy := Policy{Profile: &profile}
					client := &http.Client{Transport: Transport{Base: base, Policy: &policy}, Timeout: 2 * time.Second}
					defer client.CloseIdleConnections()
					ctx := t.Context()
					if bound {
						ctx, err = WithAddresses(ctx, "origin.test", []net.IP{net.ParseIP("127.0.0.1")})
						if err != nil {
							t.Fatal(err)
						}
					}
					get := func() error {
						request, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
						response, err := client.Do(request)
						if err != nil {
							return err
						}
						defer response.Body.Close()
						if targetTLS && response.ProtoMajor != 2 {
							t.Errorf("destination HTTP/2 lost: %s", response.Proto)
						}
						body, err := io.ReadAll(response.Body)
						if err == nil && string(body) != "ok" {
							t.Errorf("unexpected response %q", body)
						}
						return err
					}
					err := get()
					if tc.wantError {
						var authority x509.UnknownAuthorityError
						if !errors.As(err, &authority) {
							t.Fatalf("expected certificate rejection, got %v", err)
						}
						stage := "proxy-tls"
						if tc.skip && !tc.trustOrigin {
							stage = "target-tls"
						}
						if got := FailureStage(err); got != stage {
							t.Fatalf("certificate failure stage: got %s, want %s: %v", got, stage, err)
						}
					} else {
						if err != nil {
							t.Fatal(err)
						}
						if err := get(); err != nil {
							t.Fatal(err)
						}
						if dials.Load() != 1 {
							t.Fatalf("proxy connection reuse lost: dials=%d", dials.Load())
						}
						if tc.skip && !tc.trustProxy {
							profile.InsecureSkipVerify = false
							if err := get(); err == nil {
								t.Fatal("enabling verification reused the unverified pool")
							}
						}
					}
					client.CloseIdleConnections()
					mu.Lock()
					observed := append([]*handshakeTrackedConn(nil), sockets...)
					mu.Unlock()
					for _, conn := range observed {
						select {
						case <-conn.closed:
						case <-time.After(time.Second):
							t.Fatal("native socket wrapper did not close")
						}
					}
					if base.TLSClientConfig.InsecureSkipVerify != tc.sdkInsecure {
						t.Fatal("proxy option mutated destination TLS configuration")
					}
				})
			}
		}
	}
}

func TestProxyTLSVerificationSettings(t *testing.T) {
	profile := Profile{ID: "proxy", Name: "Proxy", Protocol: "https", Server: "entry.test", Port: 443, Authentication: "none"}
	ctx := WithPolicy(t.Context(), Policy{Profile: &profile})
	verified := Fingerprint(Policy{Profile: &profile})
	profile.InsecureSkipVerify = true
	if err := profile.Validate(); err != nil {
		t.Fatal(err)
	}
	if verified == Fingerprint(Policy{Profile: &profile}) {
		t.Fatal("TLS verification is missing from pool identity")
	}
	snapshot, err := FromContext(ctx)
	if err != nil || snapshot.Profile.InsecureSkipVerify {
		t.Fatalf("existing network snapshot changed: %+v %v", snapshot, err)
	}
	for _, protocol := range []string{"http", "socks5"} {
		profile.Protocol = protocol
		if err := profile.Validate(); err == nil {
			t.Fatalf("accepted HTTPS certificate opt-out for %s", protocol)
		}
	}
}
