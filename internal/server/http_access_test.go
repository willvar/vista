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

func TestHostAndOriginProtectionWithoutToken(t *testing.T) {
	app := &App{}
	handler := app.host(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tc := range []struct {
		name, host, method, origin string
		want                       int
	}{
		{"loopback read", "127.0.0.1:5186", http.MethodGet, "", http.StatusNoContent},
		{"same-origin write", "127.0.0.1:5186", http.MethodPost, "http://127.0.0.1:5186", http.StatusNoContent},
		{"local CLI write", "127.0.0.1:5186", http.MethodPost, "", http.StatusNoContent},
		{"loopback IPv6 write", "[::1]:5186", http.MethodPost, "http://[::1]:5186", http.StatusNoContent},
		{"DNS rebinding host", "example.com:5186", http.MethodGet, "", http.StatusForbidden},
		{"non-loopback host", "192.0.2.1:5186", http.MethodGet, "", http.StatusForbidden},
		{"foreign-origin write", "127.0.0.1:5186", http.MethodPost, "https://example.com", http.StatusForbidden},
		{"cross-port write", "127.0.0.1:5186", http.MethodPatch, "http://127.0.0.1:5187", http.StatusForbidden},
		{"opaque-origin write", "127.0.0.1:5186", http.MethodDelete, "null", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, "http://"+tc.host+"/api/preferences", nil)
			if tc.origin != "" {
				request.Header.Set("Origin", tc.origin)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("HTTP %d, want %d", response.Code, tc.want)
			}
		})
	}
}
