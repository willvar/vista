package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vista/internal/store"
)

func TestLocalAPIWorksWithoutToken(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := &App{store: db, started: time.Now()}
	handler := app.handler()
	for _, path := range []string{"/api/status", "/api/models", "/api/sessions", "/api/preferences"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5186"+path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s without token: HTTP %d %s", path, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPatch, "http://127.0.0.1:5186/api/preferences", strings.NewReader(`{"lastMode":"auto"}`))
	request.Header.Set("Origin", "http://127.0.0.1:5186")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	prefs, err := db.Preferences(context.Background())
	if response.Code != http.StatusOK || err != nil || prefs.LastMode != "auto" {
		t.Fatalf("PATCH without token: HTTP %d, preferences=%+v, err=%v", response.Code, prefs, err)
	}
}

func TestOpenHTTPAccessWithoutToken(t *testing.T) {
	for _, tc := range []struct {
		name, host, method, origin string
	}{
		{"loopback read", "127.0.0.1:5186", http.MethodGet, ""},
		{"same-origin write", "127.0.0.1:5186", http.MethodPost, "http://127.0.0.1:5186"},
		{"CLI write", "127.0.0.1:5186", http.MethodPost, ""},
		{"IPv6 write", "[::1]:5186", http.MethodPost, "http://[::1]:5186"},
		{"domain host", "example.com:5186", http.MethodGet, ""},
		{"LAN host", "192.0.2.1:5186", http.MethodGet, ""},
		{"HTTPS reverse proxy", "vista.example", http.MethodPost, "https://vista.example"},
		{"cross-origin write", "127.0.0.1:5186", http.MethodPost, "https://example.com"},
		{"cross-port write", "127.0.0.1:5186", http.MethodPatch, "http://127.0.0.1:5187"},
		{"opaque-origin write", "127.0.0.1:5186", http.MethodDelete, "null"},
		{"preflight", "vista.example", http.MethodOptions, "https://example.com"},
		{"opaque preflight", "192.0.2.1:5186", http.MethodOptions, "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			handler := cors(cors(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			})))
			request := httptest.NewRequest(tc.method, "http://"+tc.host+"/api/preferences", nil)
			if tc.origin != "" {
				request.Header.Set("Origin", tc.origin)
			}
			request.Header.Set("Access-Control-Request-Method", http.MethodPatch)
			request.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type, X-Vista-Language, X-Custom")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusNoContent || called != (tc.method != http.MethodOptions) {
				t.Fatalf("HTTP %d, handler called=%v", response.Code, called)
			}
			origin := tc.origin
			if origin == "" {
				origin = "*"
			}
			if response.Header().Get("Access-Control-Allow-Origin") != origin || response.Header().Get("Access-Control-Allow-Credentials") != "true" {
				t.Fatalf("invalid CORS response: %v", response.Header())
			}
			if tc.method == http.MethodOptions {
				if response.Header().Get("Access-Control-Allow-Methods") != http.MethodPatch || response.Header().Get("Access-Control-Allow-Headers") != request.Header.Get("Access-Control-Request-Headers") || response.Header().Get("Access-Control-Max-Age") != "3600" || response.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("invalid preflight response: %v", response.Header())
				}
			} else if response.Header().Get("Vary") != "Origin" {
				t.Fatalf("invalid origin cache variation: %v", response.Header())
			}
		})
	}
}
