package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vista/licenses"
)

func TestLicensesAccompanyBrowserAssets(t *testing.T) {
	app := &App{}
	asset := httptest.NewRecorder()
	app.handler().ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5186/app.js", nil))
	if asset.Code != http.StatusOK || !strings.HasPrefix(asset.Body.String(), "/*! Vista and third-party licenses: /licenses */") {
		t.Fatal("browser JavaScript does not point to its license notices")
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		response := httptest.NewRecorder()
		app.handler().ServeHTTP(response, httptest.NewRequest(method, "http://127.0.0.1:5186/licenses", nil))
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Fatalf("%s /licenses: HTTP %d, headers %v", method, response.Code, response.Header())
		}
		if method == http.MethodGet && response.Body.String() != licenses.Text {
			t.Fatal("browser license document differs from the embedded notices")
		}
		if method == http.MethodHead && response.Body.Len() != 0 {
			t.Fatal("HEAD returned a body")
		}
	}
	for _, attribution := range []string{"Copyright (c) 2026 willvar", "Mario Zechner", "Amazon.com", "models.dev"} {
		if !strings.Contains(licenses.Text, attribution) {
			t.Fatalf("missing attribution %q", attribution)
		}
	}
}
