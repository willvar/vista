package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"vista/internal/network"
)

type environmentTransportFunc func(*http.Request) (*http.Response, error)

func (f environmentTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func environmentResponse(status int, body string) (*http.Response, error) {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestEnvironmentCheckEvidenceAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name    string
		family  int
		primary bool
		witness string
		want    string
	}{
		{"IPv4 exact body", 0, true, "127.0.0.1:30000", "verified"},
		{"IPv4 matched fallback peer", 0, false, "8.8.8.8:30000", "verified"},
		{"IPv4 forwarded header is not a witness", 0, false, "127.0.0.1:30000", "no_callback"},
		{"IPv6 fallback", 1, false, "[2606:4700:4700::1111]:30000", "verified"},
		{"IPv6 no local witness", 1, false, "", "no_callback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &App{}
			const port = 8080
			target := [2]string{"1.1.1.1", "2606:4700:4700::1001"}[tc.family]
			var primaryPath, backupPath, proof, backupURL string
			lookups, callbacks := 0, 0
			callback := func(path string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.RemoteAddr = tc.witness
				req.Header.Set("X-Forwarded-For", "8.8.8.8")
				rec := httptest.NewRecorder()
				a.environmentCheck.callbackHTTP(rec, req, tc.family)
				return rec
			}
			useBackup := func(raw string) {
				t.Helper()
				parsed, err := url.Parse(raw)
				if err != nil || parsed.Scheme != "http" || parsed.Hostname() != target || parsed.Port() != "8080" || parsed.Path == primaryPath {
					t.Fatalf("invalid backup target: %q, %v", raw, err)
				}
				backupPath, backupURL = parsed.Path, raw
				if callback(primaryPath).Code != http.StatusNotFound {
					t.Fatal("previous challenge survived fallback")
				}
				if tc.witness != "" && callback(backupPath).Code != http.StatusOK {
					t.Fatal("backup token rejected")
				}
			}
			lookup := &http.Client{Transport: environmentTransportFunc(func(r *http.Request) (*http.Response, error) {
				lookups++
				if lookups == 1 && !tc.primary {
					return environmentResponse(503, "")
				}
				return environmentResponse(200, target+"\n")
			})}
			client := &http.Client{Transport: environmentTransportFunc(func(r *http.Request) (*http.Response, error) {
				callbacks++
				switch r.URL.String() {
				case environmentGlobalping:
					var request struct {
						Target             string
						MeasurementOptions struct {
							Port    int
							Request struct{ Path string }
						}
					}
					if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&request) != nil || request.Target != target || request.MeasurementOptions.Port != port {
						t.Fatal("wrong primary target")
					}
					primaryPath = request.MeasurementOptions.Request.Path
					if !tc.primary {
						return environmentResponse(429, "")
					}
					response := callback(primaryPath)
					if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
						t.Fatal("missing temporary proof")
					}
					proof = response.Body.String()
					return environmentResponse(202, `{"id":"test-1"}`)
				case environmentGlobalping + "/test-1":
					body, _ := json.Marshal(map[string]any{"target": target, "status": "finished", "results": []any{map[string]any{"result": map[string]any{
						"status": "finished", "statusCode": 200, "resolvedAddress": target, "rawBody": proof, "truncated": false,
					}}}})
					return environmentResponse(200, string(body))
				case environmentCheckHost + "/check-result-extended/backup1":
					body, _ := json.Marshal(map[string]any{"command": "http", "host": backupURL, "results": map[string]any{"de1": [][]any{{1, 0.1, "OK", "200", target}}}})
					return environmentResponse(200, string(body))
				}
				switch r.URL.Host {
				case "check-host.net":
					useBackup(r.URL.Query().Get("host"))
					return environmentResponse(200, `{"ok":1,"request_id":"backup1","nodes":{"de1":["de","Germany","Berlin","8.8.8.8"]}}`)
				case "ipcow.com":
					useBackup(r.URL.Query().Get("url"))
					body, _ := json.Marshal(map[string]any{"results": map[string]any{"ipv6": map[string]any{"stack": "ipv6", "ok": true, "status": 200, "final_url": backupURL}}})
					return environmentResponse(200, string(body))
				}
				t.Fatalf("unexpected outbound request: %s", r.URL)
				return nil, nil
			})}
			if got := environmentLookup(t.Context(), lookup, tc.family); got.String() != target {
				t.Fatalf("address %s, want %s", got, target)
			}
			if got := a.environmentCheck.measurePort(t.Context(), client, netip.MustParseAddr(target), tc.family, port); got != tc.want {
				t.Fatalf("status %s, want %s", got, tc.want)
			}
			if tc.primary {
				if lookups != 1 || callbacks != 2 {
					t.Fatalf("primary success dispatched extra work: %d/%d", lookups, callbacks)
				}
			} else if lookups != 2 || backupPath == "" || callback(backupPath).Code != http.StatusNotFound {
				t.Fatal("missing fallback or undeleted challenge")
			}
			if callback(primaryPath).Code != http.StatusNotFound {
				t.Fatal("undeleted primary challenge")
			}
		})
	}
}

func TestEnvironmentCheckClientKeepsExplicitDirectRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addr, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil || !addr.Addr().Is4() || !addr.Addr().IsLoopback() {
			t.Errorf("expected a direct IPv4 loopback connection: %v, %v", addr, err)
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/other", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "local fixture")
	}))
	defer server.Close()
	// A malformed inherited proxy would reject the request if used instead of
	// the diagnostic's explicitly pinned Direct policy.
	ctx := network.WithPolicy(t.Context(), network.Policy{Profile: &network.Profile{Protocol: "invalid"}})
	client := newEnvironmentClient("tcp4")
	defer client.CloseIdleConnections()
	if body, err := environmentFetch(ctx, client, server.URL, nil, 64); err != nil || string(body) != "local fixture" {
		t.Fatalf("direct lookup returned %q, %v", body, err)
	}
	if _, err := environmentFetch(ctx, client, server.URL+"/redirect", nil, 64); err == nil {
		t.Fatal("diagnostic followed an HTTP redirect")
	}
}

func TestEnvironmentCheckRejectsInjectedTargets(t *testing.T) {
	a := &App{listenerPort: func() int { t.Fatal("unexpected check execution"); return 5187 }}
	for _, tc := range []struct {
		path, body, site string
		code             int
	}{
		{"/api/environment-check", "{}", "same-origin", 400},
		{"/api/environment-check?port=80", "", "same-origin", 400},
		{"/api/environment-check", "", "cross-site", 403},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Sec-Fetch-Site", tc.site)
		w := httptest.NewRecorder()
		a.handler().ServeHTTP(w, req)
		if w.Code != tc.code {
			t.Fatalf("%s / %s: got %d want %d", tc.path, tc.site, w.Code, tc.code)
		}
	}
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "100.64.0.1", "192.0.2.1", "::1", "::ffff:8.8.8.8", "64:ff9b::808:808", "fc00::1", "2001:db8::1"} {
		if environmentPublicIP(netip.MustParseAddr(raw)) {
			t.Errorf("accepted non-public address: %s", raw)
		}
	}
}

// They are the real production port numbers in the provider requests, but the test
// listener maps them to private loopback sockets so no service ports are occupied by CI.
func TestEnvironmentCheckFallbackPortBounds(t *testing.T) {
	common := []int{80, 443, 8080, 8443, 3306, 5432}
	for _, tc := range []struct{ vistaPort, fallback int }{
		{0, 0}, {79, 0}, {15888, 15889}, {65534, 65535}, {65535, 0},
	} {
		a := &App{listenerPort: func() int { return tc.vistaPort }}
		want := append([]int(nil), common...)
		if tc.fallback != 0 {
			want = append(want, tc.fallback)
		}
		if got := a.environmentCandidatePorts(); !reflect.DeepEqual(got, want) {
			t.Errorf("Vista port %d: candidate ports %v, want %v", tc.vistaPort, got, want)
		}
	}
}

func TestEnvironmentCheckFixedPortPlan(t *testing.T) {
	t.Run("common success stops", func(t *testing.T) {
		testEnvironmentPortPlan(t, true, []int{80, 443})
	})
	t.Run("failed callback keeps both probes on the first bindable port", func(t *testing.T) {
		testEnvironmentPortPlan(t, false, []int{80, 443})
	})
	t.Run("fallback requires all common ports to fail binding", func(t *testing.T) {
		testEnvironmentPortPlan(t, true, []int{80, 443, 8080, 8443, 3306, 5432, 15889})
	})
}

func testEnvironmentPortPlan(t *testing.T, verified bool, wantBinds []int) {
	t.Helper()
	selected, skipped := wantBinds[len(wantBinds)-1], wantBinds[:len(wantBinds)-1]
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	var attempted, probed []int
	var backupPorts, steps []string
	var addr, proof string
	check := &App{listenerPort: func() int { return 15888 }}
	admin := check.handler()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	assertClosed := func(address string) {
		if conn, err := net.DialTimeout("tcp4", address, 200*time.Millisecond); err == nil {
			_ = conn.Close()
			t.Errorf("test listener remained open at %s", address)
		}
	}
	listen := func(ctx context.Context, network, address string) (net.Listener, error) {
		if !reflect.DeepEqual(steps, []string{"address", "port"}) {
			t.Errorf("starting bind at the wrong step: %v", steps)
		}
		if network != "tcp4" {
			t.Errorf("unexpected bind network: %s", network)
		}
		if addr != "" {
			assertClosed(addr)
		}
		port, err := strconv.Atoi(strings.TrimPrefix(address, ":"))
		if err != nil {
			return nil, err
		}
		attempted = append(attempted, port)
		var config net.ListenConfig
		for _, busy := range skipped {
			if port == busy {
				return config.Listen(ctx, "tcp4", occupied.Addr().String())
			}
		}
		ln, err := config.Listen(ctx, "tcp4", "127.0.0.1:0")
		if err == nil {
			addr = ln.Addr().String()
		}
		return ln, err
	}
	lookup := &http.Client{Transport: environmentTransportFunc(func(r *http.Request) (*http.Response, error) {
		if !reflect.DeepEqual(steps, []string{"address"}) {
			t.Errorf("starting address lookup at the wrong step: %v", steps)
		}
		if r.URL.String() == environmentIPs[0][0] {
			return environmentResponse(200, "1.1.1.1")
		}
		return environmentResponse(503, "")
	})}
	probes := &http.Client{Transport: environmentTransportFunc(func(r *http.Request) (*http.Response, error) {
		if !reflect.DeepEqual(steps, []string{"address", "port", "callback"}) {
			t.Errorf("starting external probe at the wrong step: %v", steps)
		}
		if r.URL.String() == environmentGlobalping+"/ok" {
			if !verified {
				return environmentResponse(200, `{"status":"finished","target":"1.1.1.1","results":[]}`)
			}
			return environmentResponse(200, `{"status":"finished","target":"1.1.1.1","results":[{"result":{"status":"finished","statusCode":200,"resolvedAddress":"1.1.1.1","truncated":false,"rawBody":`+strconv.Quote(proof)+`}}]}`)
		}
		if r.URL.Host == "check-host.net" {
			target, err := url.Parse(r.URL.Query().Get("host"))
			if err != nil {
				return nil, err
			}
			backupPorts = append(backupPorts, target.Port())
			return environmentResponse(503, "")
		}
		var req struct {
			MeasurementOptions struct {
				Port    int
				Request struct{ Path string }
			}
		}
		if r.URL.String() != environmentGlobalping || r.Method != http.MethodPost {
			return nil, fmt.Errorf("unexpected URL %s", r.URL)
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return nil, err
		}
		mo := req.MeasurementOptions
		probed = append(probed, mo.Port)
		if mo.Port != selected {
			return environmentResponse(503, "")
		}
		// The administrative HTTP handler must not expose the live callback.
		rec := httptest.NewRecorder()
		admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, mo.Request.Path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("callback reachable on the management handler: %d", rec.Code)
		}
		res, err := client.Get("http://" + addr + "/api/status")
		if err != nil {
			return nil, err
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("temporary listener served an application endpoint: %d", res.StatusCode)
		}
		res, err = client.Get("http://" + addr + mo.Request.Path)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != http.StatusOK || len(body) == 0 {
			t.Errorf("expected temporary callback proof, code=%d err=%v", res.StatusCode, err)
			return environmentResponse(503, "")
		}
		proof = string(body)
		return environmentResponse(202, `{"id":"ok"}`)
	})}
	got := check.environmentCheck.scan(t.Context(), [2]*http.Client{lookup, lookup}, probes, check.environmentCandidatePorts(), listen, func(step string) { steps = append(steps, step) })
	if !reflect.DeepEqual(steps, []string{"address", "port", "callback"}) {
		t.Fatalf("reported steps %v", steps)
	}
	if !reflect.DeepEqual(attempted, wantBinds) || !reflect.DeepEqual(probed, []int{selected}) {
		t.Fatalf("single-port plan failed: binds=%v primary=%v", attempted, probed)
	}
	var wantBackup []string
	want := [2]environmentResult{
		{Family: "IPv4", Status: "no_callback", AttemptedPorts: []int{selected}},
		{Family: "IPv6", Status: "no_address"},
	}
	if verified {
		want[0].Status, want[0].Port = "verified", selected
	} else {
		wantBackup = []string{strconv.Itoa(selected)}
	}
	if !reflect.DeepEqual(backupPorts, wantBackup) {
		t.Fatalf("backup ports %v, want %v", backupPorts, wantBackup)
	}
	if !reflect.DeepEqual(got.Results, want) {
		t.Fatalf("unexpected report: %+v, want %+v", got.Results, want)
	}
	assertClosed(addr)
	conn, err := net.DialTimeout("tcp4", occupied.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("original listener no longer reachable: %v", err)
	}
	_ = conn.Close()
}

func TestEnvironmentCheckCancelClosesListener(t *testing.T) {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback not available: %v", err)
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var requests int
	var peer net.Conn
	client := &http.Client{Transport: environmentTransportFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		var err error
		peer, err = net.DialTimeout("tcp6", ln.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = peer.Close() })
		if _, err := io.WriteString(peer, "GET / HTTP/1.1\r\nHost: incomplete"); err != nil {
			t.Fatal(err)
		}
		cancel()
		return nil, r.Context().Err()
	})}
	check := &environmentCheck{}
	if status := check.servePort(ctx, client, ln, netip.MustParseAddr("2606:4700:4700::1111"), 1, ln.Addr().(*net.TCPAddr).Port); status != "probe_unavailable" || requests != 1 {
		t.Fatalf("cancellation: status=%q requests=%d", status, requests)
	}
	if _, err := ln.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("listener not closed: %v", err)
	}
	if len(check.challenges) != 0 {
		t.Fatal("challenge survived cancellation")
	}
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var timeout net.Error
	if _, err := peer.Read(make([]byte, 1)); err == nil || (errors.As(err, &timeout) && timeout.Timeout()) {
		t.Fatalf("accepted connection did not close after cancellation: %v", err)
	}
}

func TestEnvironmentCheckStopsAtUnavailableStage(t *testing.T) {
	for _, tc := range []struct {
		status string
		steps  []string
	}{
		{"no_address", []string{"address"}},
		{"no_listener", []string{"address", "port"}},
	} {
		t.Run(tc.status, func(t *testing.T) {
			check := &environmentCheck{}
			lookup := &http.Client{Transport: environmentTransportFunc(func(r *http.Request) (*http.Response, error) {
				if tc.status == "no_listener" && r.URL.String() == environmentIPs[0][0] {
					return environmentResponse(200, "1.1.1.1")
				}
				return environmentResponse(503, "")
			})}
			listen := func(context.Context, string, string) (net.Listener, error) {
				if tc.status == "no_address" {
					t.Error("tried to bind without an address")
				}
				return nil, net.ErrClosed
			}
			var steps []string
			got := check.scan(t.Context(), [2]*http.Client{lookup, lookup}, nil, []int{80}, listen, func(step string) { steps = append(steps, step) })
			if got.Results[0].Status != tc.status || len(got.Results[0].AttemptedPorts) != 0 || !reflect.DeepEqual(steps, tc.steps) {
				t.Fatalf("result=%+v, steps=%v, want status=%s and steps=%v", got.Results[0], steps, tc.status, tc.steps)
			}
		})
	}
}

func TestEnvironmentCheckStreamsBeforeCompletion(t *testing.T) {
	advance := make(chan struct{})
	app := &App{}
	srv := httptest.NewServer(app.stateMutations(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		streamEnvironmentCheck(w, ctx, cancel, func(progress func(string)) environmentReport {
			for _, step := range []string{"address", "port", "callback"} {
				progress(step)
				select {
				case <-advance:
				case <-ctx.Done():
					return environmentReport{}
				}
			}
			return environmentReport{Results: [2]environmentResult{{Family: "IPv4", Status: "verified"}, {Family: "IPv6", Status: "no_address"}}}
		})
	})))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/environment-check", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" || res.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("unexpected stream response: %d %v", res.StatusCode, res.Header)
	}
	frames := bufio.NewScanner(res.Body)
	read := func() environmentUpdate {
		t.Helper()
		if !frames.Scan() {
			t.Fatalf("missing streamed frame: %v", frames.Err())
		}
		payload, ok := strings.CutPrefix(frames.Text(), "data: ")
		var update environmentUpdate
		if !ok || json.Unmarshal([]byte(payload), &update) != nil || !frames.Scan() || frames.Text() != "" {
			t.Fatalf("invalid streamed frame: %q", payload)
		}
		return update
	}
	for _, want := range []string{"address", "port", "callback"} {
		if got := read(); got.Step != want || got.Report != nil {
			t.Fatalf("step = %+v, want %q", got, want)
		}
		select {
		case advance <- struct{}{}:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if got := read(); got.Step != "" || got.Report == nil || got.Report.Results[0].Status != "verified" {
		t.Fatalf("invalid final report: %+v", got)
	}
}
