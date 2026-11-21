package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/text/encoding/simplifiedchinese"

	"vista/internal/network"
	"vista/internal/workerproto"
)

func TestLocalWebReadingAndConversion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<html><body><h1>Internal status</h1><p>Ready.</p><a href='/details'>Details</a><script>ignored()</script></body></html>")
	}))
	defer server.Close()
	var output strings.Builder
	page, err := Webfetch(network.WithPolicy(context.Background(), network.Policy{}), server.URL, "", &output, workerproto.WebfetchConfig{})
	if err != nil || page.Reader != "vista" || page.Format != "markdown" || !strings.Contains(output.String(), "Internal status") || !strings.Contains(output.String(), server.URL+"/details") {
		t.Fatalf("local markdown: page=%+v err=%v output=%q", page, err, output.String())
	}
	output.Reset()
	page, err = Webfetch(network.WithPolicy(context.Background(), network.Policy{}), server.URL, "text", &output, workerproto.WebfetchConfig{})
	if err != nil || page.Format != "text" || strings.Contains(output.String(), "ignored()") {
		t.Fatalf("local text: %v %q", err, output.String())
	}
}

func TestLocalWebReadingIPv6OnlyLocalhost(t *testing.T) {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "IPv6 internal page")
	})}
	go server.Serve(listener)
	defer server.Close()
	policy := network.Policy{}
	var output strings.Builder
	url := fmt.Sprintf("http://localhost:%d/", listener.Addr().(*net.TCPAddr).Port)
	page, err := Webfetch(network.WithPolicy(context.Background(), policy), url, "text", &output, workerproto.WebfetchConfig{Local: true})
	if err != nil || page.Reader != "vista" || !strings.Contains(output.String(), "IPv6 internal page") {
		t.Fatalf("IPv6 local validation/connection : page=%+v err=%v output=%q", page, err, output.String())
	}
}

func TestLocalWebCharsetDecoding(t *testing.T) {
	gbk := func(source string) string {
		t.Helper()
		encoded, err := simplifiedchinese.GBK.NewEncoder().String(source)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	for _, check := range []struct {
		name, contentType, body, want string
	}{
		{"HTML meta GBK", "text/html", gbk("<meta charset=gbk><p>中文</p>"), "中文"},
		{"HTML http-equiv GBK", "text/html", gbk(`<meta http-equiv="Content-Type" content="text/html; charset=gbk"><p>中文</p>`), "中文"},
		{"HTML header overrides meta", "text/html; charset=gbk", gbk("<meta charset=utf-8><p>中文</p>"), "中文"},
		{"HTML meta UTF-8", "text/html", "<meta charset=utf-8><p>中文</p>", "中文"},
		{"HTML undeclared UTF-8", "text/html", "<p>中文</p>", "中文"},
		{"empty HTML", "text/html", "", ""},
		{"text defaults to UTF-8", "text/plain", "中文 <meta charset=windows-1252>", "中文 <meta charset=windows-1252>"},
		{"JSON defaults to UTF-8", "application/json", `{"text":"中文 <meta charset=windows-1252>"}`, `{"text":"中文 <meta charset=windows-1252>"}`},
		{"text header GBK", "text/plain; charset=gbk", gbk("中文"), "中文"},
	} {
		t.Run(check.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", check.contentType)
				io.WriteString(w, check.body)
			}))
			defer server.Close()
			for _, format := range []string{"", "text"} {
				t.Run("format="+format, func(t *testing.T) {
					var output strings.Builder
					page, err := Webfetch(network.WithPolicy(context.Background(), network.Policy{}), server.URL, format, &output, workerproto.WebfetchConfig{Local: true})
					if err != nil {
						t.Fatal(err)
					}
					if page.Reader != "vista" || strings.TrimSpace(output.String()) != check.want {
						t.Fatalf("reader=%s output=%q, want %q", page.Reader, output.String(), check.want)
					}
				})
			}
		})
	}
}

func TestLocalFetchFailureNeverCallsReaders(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: webfetchTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Hostname() != "127.0.0.1" {
			t.Fatal("private request reached external reader")
		}
		return webfetchResponseFixture(r, 503, "text/plain", "unavailable"), nil
	})}
	var output strings.Builder
	if _, err := webfetchWithClient(context.Background(), "http://127.0.0.1/status", "", &output, client, workerproto.WebfetchConfig{DeepSeekAPIKey: "test-key"}); err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
func TestAuthorizedSensitiveLocalURLIsRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"ok"}`)
	}))
	defer server.Close()
	var output strings.Builder
	target := server.URL + "/?token=temporary-token"
	if _, err := Webfetch(context.Background(), target, "", &output, workerproto.WebfetchConfig{}); err == nil {
		t.Fatal("sensitive URL was accepted implicitly")
	}
	page, err := Webfetch(context.Background(), target, "", &output, workerproto.WebfetchConfig{Local: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(page.URL, "temporary-token") || page.Reader != "vista" {
		t.Fatal("sensitive URL was not redacted")
	}
}
func TestLocalRedirectCannotReachMetadata(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, "http://169.254.169.254/metadata", http.StatusFound)
	}))
	defer server.Close()
	var output strings.Builder
	if _, err := Webfetch(context.Background(), server.URL, "", &output, workerproto.WebfetchConfig{}); err == nil || calls != 1 {
		t.Fatalf("unsafe redirect accepted: calls=%d err=%v", calls, err)
	}
}

func TestLocalRedirectReferrerPolicy(t *testing.T) {
	var referer string
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		referer = r.Referer()
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "redirected page")
	}))
	defer destination.Close()
	var target string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			referer = r.Referer()
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "same-origin page")
			return
		}
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer origin.Close()
	for _, check := range []struct {
		name, target string
		sameOrigin   bool
	}{
		{"same origin", origin.URL + "/final", true},
		{"different port", destination.URL + "/final", false},
		{"different host", strings.Replace(destination.URL, "127.0.0.1", "localhost", 1) + "/final", false},
	} {
		t.Run(check.name, func(t *testing.T) {
			target, referer = check.target, ""
			original := origin.URL + "/private?token=synthetic-token"
			var output strings.Builder
			if _, err := Webfetch(context.Background(), original, "", &output, workerproto.WebfetchConfig{Local: true}); err != nil {
				t.Fatal(err)
			}
			want := ""
			if check.sameOrigin {
				want = original
			}
			if referer != want || output.Len() == 0 {
				t.Fatalf("redirect referrer = %q, want %q; body bytes = %d", referer, want, output.Len())
			}
		})
	}
}

func webDNSFixture(t *testing.T, respond func(*dnsmessage.Message) bool) {
	t.Helper()
	resolver, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		buffer := make([]byte, 4096)
		for {
			n, peer, err := resolver.ReadFrom(buffer)
			if err != nil {
				return
			}
			var query dnsmessage.Message
			if err := query.Unpack(buffer[:n]); err != nil || len(query.Questions) != 1 {
				t.Errorf("invalid DNS query: %v", err)
				continue
			}
			answer := dnsmessage.Message{Header: dnsmessage.Header{ID: query.ID, Response: true, RecursionAvailable: true}, Questions: query.Questions}
			if !respond(&answer) {
				continue
			}
			wire, err := answer.Pack()
			if err != nil {
				t.Error(err)
				continue
			}
			if _, err := resolver.WriteTo(wire, peer); err != nil {
				return
			}
		}
	}()
	previous := net.DefaultResolver
	address := resolver.LocalAddr().String()
	// A DNS exchange may finish after the canceled lookup returns. Its dialer
	// must not read DefaultResolver while cleanup restores it.
	dialer := net.Dialer{Resolver: new(net.Resolver)}
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "udp", address)
	}}
	t.Cleanup(func() {
		net.DefaultResolver = previous
		resolver.Close()
		<-stopped
	})
}

func TestWebTargetDNSCompletenessAndPrivatePermissions(t *testing.T) {
	for _, check := range []struct {
		name, ipv4, ipv6, failure     string
		fail4, fail6, local, explicit bool
	}{
		{name: "IPv4 with empty AAAA", ipv4: "192.0.2.1"},
		{name: "IPv6 with empty A", ipv6: "2001:db8::1"},
		{name: "both families", ipv4: "192.0.2.1", ipv6: "2001:db8::1"},
		{name: "IPv4 with failed AAAA", ipv4: "192.0.2.1", fail6: true, local: true},
		{name: "IPv6 with failed A", ipv6: "2001:db8::1", fail4: true, local: true},
		{name: "both failed", fail4: true, fail6: true, failure: "could not resolve"},
		{name: "both empty", failure: "could not resolve"},
		{name: "partial private needs permission", ipv4: "10.1.2.3", fail6: true, failure: "explicitly choose local"},
		{name: "partial restricted rejects", ipv4: "169.254.169.254", fail6: true, failure: "restricted"},
		{name: "explicit private partial", ipv4: "10.1.2.3", fail6: true, local: true, explicit: true},
	} {
		t.Run(check.name, func(t *testing.T) {
			webDNSFixture(t, func(answer *dnsmessage.Message) bool {
				question := answer.Questions[0]
				if question.Type == dnsmessage.TypeA {
					if check.fail4 {
						answer.Header.RCode = dnsmessage.RCodeServerFailure
					} else if check.ipv4 != "" {
						var ip [4]byte
						copy(ip[:], net.ParseIP(check.ipv4).To4())
						answer.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: question.Name, Type: question.Type, Class: dnsmessage.ClassINET}, Body: &dnsmessage.AResource{A: ip}}}
					}
				} else if question.Type == dnsmessage.TypeAAAA {
					if check.fail6 {
						answer.Header.RCode = dnsmessage.RCodeServerFailure
					} else if check.ipv6 != "" {
						var ip [16]byte
						copy(ip[:], net.ParseIP(check.ipv6).To16())
						answer.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: question.Name, Type: question.Type, Class: dnsmessage.ClassINET}, Body: &dnsmessage.AAAAResource{AAAA: ip}}}
					}
				}
				return true
			})
			ctx, cancel := context.WithTimeout(network.WithPolicy(context.Background(), network.Policy{}), 3*time.Second)
			defer cancel()
			target, err := classifyWebTarget(ctx, "http://public-shaped.example/page", check.explicit)
			if check.failure != "" {
				if err == nil || !strings.Contains(err.Error(), check.failure) {
					t.Fatalf("classification error=%v, want %s", err, check.failure)
				}
				return
			}
			if err != nil || target.local != check.local || target.allowPrivate != check.explicit {
				t.Fatalf("classification=%+v error=%v, want local=%t allowPrivate=%t", target, err, check.local, check.explicit)
			}
			addresses, want := map[string]bool{}, map[string]bool{}
			for _, ip := range target.addresses {
				addresses[ip.String()] = true
			}
			for _, ip := range []string{check.ipv4, check.ipv6} {
				if ip != "" {
					want[ip] = true
				}
			}
			if !reflect.DeepEqual(addresses, want) {
				t.Fatalf("addresses=%v, want %v", addresses, want)
			}
		})
	}
}

func TestWebTargetCancellationDoesNotAcceptPartialDNS(t *testing.T) {
	queried := make(chan struct{})
	var once sync.Once
	webDNSFixture(t, func(answer *dnsmessage.Message) bool {
		question := answer.Questions[0]
		if question.Type == dnsmessage.TypeAAAA {
			once.Do(func() { close(queried) })
			return false
		}
		answer.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}, Body: &dnsmessage.AResource{A: [4]byte{192, 0, 2, 1}}}}
		return true
	})
	ctx, cancel := context.WithCancel(network.WithPolicy(context.Background(), network.Policy{}))
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := classifyWebTarget(ctx, "http://public-shaped.example/page", false)
		finished <- err
	}()
	select {
	case <-queried:
	case <-time.After(3 * time.Second):
		t.Fatal("AAAA lookup never started")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled classification returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("classification ignored cancellation")
	}
}

func TestWebfetchPartialDNSStaysLocal(t *testing.T) {
	webDNSFixture(t, func(answer *dnsmessage.Message) bool {
		question := answer.Questions[0]
		if question.Type == dnsmessage.TypeAAAA {
			answer.Header.RCode = dnsmessage.RCodeServerFailure
		} else {
			answer.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}, Body: &dnsmessage.AResource{A: [4]byte{192, 0, 2, 1}}}}
		}
		return true
	})
	for _, check := range []struct {
		name, redirect, failure string
		status, calls           int
	}{
		{name: "success", status: 200, calls: 1},
		{name: "failure has no external fallback", status: 503, calls: 1, failure: "HTTP 503"},
		{name: "public redirect", redirect: "http://192.0.2.2/page", status: 302, calls: 2},
		{name: "private redirect rejected", redirect: "http://127.0.0.1/page", status: 302, calls: 1, failure: "non-public address"},
	} {
		t.Run(check.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: webfetchTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Hostname() != "public-shaped.example" && r.URL.Hostname() != "192.0.2.2" {
					t.Fatalf("request reached unapproved destination: %s", r.URL)
				}
				status := check.status
				if calls > 1 {
					status = 200
				}
				response := webfetchResponseFixture(r, status, "text/plain", "Validated page content")
				response.Header.Set("Location", check.redirect)
				return response, nil
			})}
			var output strings.Builder
			page, err := webfetchWithClient(network.WithPolicy(context.Background(), network.Policy{}), "http://public-shaped.example/page", "text", &output, client, workerproto.WebfetchConfig{DeepSeekAPIKey: "fixture-key"})
			if calls != check.calls {
				t.Fatalf("requests=%d, want %d", calls, check.calls)
			}
			if check.failure != "" {
				if err == nil || !strings.Contains(err.Error(), check.failure) {
					t.Fatalf("error=%v, want %s", err, check.failure)
				}
			} else if err != nil || page.Reader != "vista" || output.String() != "Validated page content" {
				t.Fatalf("page=%+v error=%v output=%q", page, err, output.String())
			}
		})
	}
}

func TestLocalWebPinsIDNAAddressesAcrossValidationAndDial(t *testing.T) {
	for _, check := range []struct {
		name, host, queryName           string
		ttl                             uint32
		redirect, restricted, automatic bool
	}{
		{"Unicode zero TTL", "bücher.test", "xn--bcher-kva.test.", 0, false, false, false},
		{"Unicode redirect zero TTL", "bücher.test", "xn--bcher-kva.test.", 0, true, false, false},
		{"ASCII zero TTL", "ascii.test", "ascii.test.", 0, false, false, false},
		{"Punycode zero TTL", "xn--bcher-kva.test", "xn--bcher-kva.test.", 0, false, false, false},
		{"Unicode case and final dot", "BÜCHER.TEST.", "xn--bcher-kva.test.", 0, false, false, false},
		{"Unicode positive TTL", "bücher.test", "xn--bcher-kva.test.", 60, false, false, false},
		{"initially restricted", "bücher.test", "xn--bcher-kva.test.", 0, false, true, false},
		{"restricted redirect", "bücher.test", "xn--bcher-kva.test.", 0, true, true, false},
		{"partial automatic lookup", "partial.example", "partial.example.", 0, false, false, true},
	} {
		t.Run(check.name, func(t *testing.T) {
			var aQueries atomic.Int32
			webDNSFixture(t, func(answer *dnsmessage.Message) bool {
				question := answer.Questions[0]
				if question.Name.String() != check.queryName {
					// Native resolvers may try the host's configured search domains.
					// Only the exact normalized authority has a fixture address.
					answer.Header.RCode = dnsmessage.RCodeNameError
					return true
				}
				if question.Type == dnsmessage.TypeA {
					ip := [4]byte{192, 0, 2, 1}
					if aQueries.Add(1) > 1 || check.restricted {
						ip = [4]byte{169, 254, 169, 254}
					}
					answer.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: check.ttl}, Body: &dnsmessage.AResource{A: ip}}}
				} else if check.automatic {
					answer.Header.RCode = dnsmessage.RCodeServerFailure
				}
				return true
			})
			target := "http://" + check.host + "/page"
			allowed := map[string]bool{}
			if check.redirect {
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, "http://"+check.host+"/page", http.StatusFound)
				}))
				defer origin.Close()
				allowed[origin.Listener.Addr().String()] = true
				target = origin.URL
			}
			base := http.DefaultTransport.(*http.Transport).Clone()
			dialer := &net.Dialer{Timeout: 2 * time.Second}
			var mu sync.Mutex
			var targets []string
			base.DialContext = func(ctx context.Context, kind, address string) (net.Conn, error) {
				if allowed[address] {
					return dialer.DialContext(ctx, kind, address)
				}
				mu.Lock()
				targets = append(targets, address)
				mu.Unlock()
				return nil, errors.New("fixture target dial intercepted before socket connection")
			}
			previous := http.DefaultTransport
			http.DefaultTransport = base
			defer func() {
				network.Transport{}.CloseIdleConnections()
				base.CloseIdleConnections()
				http.DefaultTransport = previous
			}()
			policy := network.Policy{}
			ctx, cancel := context.WithTimeout(network.WithPolicy(context.Background(), policy), 5*time.Second)
			defer cancel()
			var output strings.Builder
			_, err := Webfetch(ctx, target, "text", &output, workerproto.WebfetchConfig{Local: !check.automatic})
			mu.Lock()
			dials := append([]string(nil), targets...)
			mu.Unlock()
			t.Logf("host=%q redirect=%v TTL=%d A_queries=%d dial_arguments=%v error=%v", check.host, check.redirect, check.ttl, aQueries.Load(), dials, err)
			if check.restricted {
				if err == nil || !strings.Contains(err.Error(), "restricted") || len(dials) != 0 || aQueries.Load() != 1 {
					t.Fatalf("restricted answer was not rejected before target dial: %v %v", dials, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "intercepted before socket connection") {
				t.Fatalf("fixture did not reach its pre-connection interception: %v", err)
			}
			if !reflect.DeepEqual(dials, []string{"192.0.2.1:80"}) || aQueries.Load() != 1 {
				t.Errorf("validated IP binding lost: dial arguments=%v A queries=%d; expected validated 192.0.2.1:80 without re-resolution", dials, aQueries.Load())
			}
		})
	}
}
