package server

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"vista/internal/network"
)

func TestNetworkCheckHTTPRoutes(t *testing.T) {
	for _, name := range []string{"direct", "proxy", "https-proxy", "verified-https-proxy"} {
		t.Run(name, func(t *testing.T) {
			proxied := name != "direct"
			tlsProxy := strings.Contains(name, "https-proxy")
			app := networkTestApp(t)
			initial := networkTestView(t, app)
			var received atomic.Int32
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("check forwarded business credentials")
				}
				if proxied && (!r.URL.IsAbs() || r.URL.Host != "network-check.example.invalid") {
					t.Errorf("proxy did not receive the requested target: %s", r.RequestURI)
				}
				w.Header().Set("Location", "http://must-not-follow.invalid/")
				w.WriteHeader(http.StatusFound)
			})
			var server *httptest.Server
			if tlsProxy {
				server = httptest.NewTLSServer(handler)
			} else {
				server = httptest.NewServer(handler)
			}
			defer server.Close()
			defer network.Transport{}.CloseIdleConnections()
			input := networkConfigInput{Config: network.Settings{}, Revision: initial.Revision, Target: server.URL + "/check"}
			wantRoute := "direct"
			if proxied {
				host, port, err := net.SplitHostPort(server.Listener.Addr().String())
				if err != nil {
					t.Fatal(err)
				}
				number, _ := strconv.Atoi(port)
				profile := network.Profile{ID: "test-proxy", Name: "HTTP check proxy", Protocol: "http", Server: host, Port: number, Authentication: "none"}
				if tlsProxy {
					profile.Protocol = "https"
					profile.InsecureSkipVerify = name == "https-proxy"
				}
				input.Config.Profiles = []network.Profile{profile}
				input.ProfileID, input.Target, wantRoute = profile.ID, "http://network-check.example.invalid/check", profile.Name
			}
			body, _ := json.Marshal(input)
			req := httptest.NewRequest(http.MethodPost, "/api/network-check", bytes.NewReader(body)).WithContext(t.Context())
			req.Header.Set("Authorization", "Bearer must-not-forward")
			req.Header.Set("Cookie", "business=must-not-forward")
			response := httptest.NewRecorder()
			app.networkCheckHTTP(response, req)
			var result struct {
				OK     bool   `json:"ok"`
				Status int    `json:"httpStatus"`
				Route  string `json:"route"`
				Target string `json:"target"`
				Stage  string `json:"stage"`
			}
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil {
				t.Fatalf("invalid check response: %d %s", response.Code, response.Body.String())
			}
			wantOK := name != "verified-https-proxy"
			wantStatus, wantRequests := http.StatusFound, int32(1)
			if !wantOK {
				wantStatus, wantRequests = 0, 0
			}
			if result.OK != wantOK || result.Status != wantStatus || result.Route != wantRoute || result.Target != input.Target || received.Load() != wantRequests {
				t.Fatalf("HTTP route or redirect behavior lost: %+v requests=%d", result, received.Load())
			}
			if !wantOK && result.Stage != "proxy-tls" {
				t.Fatalf("proxy certificate error was attributed to %s", result.Stage)
			}
			if after := networkTestView(t, app); after.Revision != initial.Revision || len(after.Config.Profiles) != 0 {
				t.Fatal("check saved its draft")
			}
		})
	}
}

func TestNetworkCheckRejectsInvalidTargets(t *testing.T) {
	app := networkTestApp(t)
	initial := networkTestView(t, app)
	for _, target := range []string{"", "ftp://example.invalid/check", "//example.invalid/check", "http:///check", "http://user:password@example.invalid/", "https://example.invalid/?token=secret", "http://example.invalid/#secret", "http://example.invalid:bad/"} {
		t.Run(target, func(t *testing.T) {
			body, _ := json.Marshal(networkConfigInput{Config: network.Settings{}, Revision: initial.Revision, Target: target})
			response := httptest.NewRecorder()
			app.networkCheckHTTP(response, httptest.NewRequest(http.MethodPost, "/api/network-check", bytes.NewReader(body)))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("accepted invalid target %q: %d %s", target, response.Code, response.Body.String())
			}
		})
	}
}
