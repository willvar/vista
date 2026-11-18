package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"

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
			mux := a.handler()
			const port = 5187
			target := [2]string{"1.1.1.1", "2606:4700:4700::1001"}[tc.family]
			var primaryPath, backupPath, proof, backupURL string
			lookups, callbacks := 0, 0
			callback := func(path string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.RemoteAddr = tc.witness
				req.Header.Set("X-Forwarded-For", "8.8.8.8")
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				return rec
			}
			useBackup := func(raw string) {
				t.Helper()
				parsed, err := url.Parse(raw)
				if err != nil || parsed.Scheme != "http" || parsed.Hostname() != target || parsed.Port() != "5187" || parsed.Path == primaryPath {
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
			if got := a.environmentCheck.measure(t.Context(), lookup, client, tc.family, port); got != tc.want {
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
