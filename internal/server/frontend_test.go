package server

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func frontendRequest(t *testing.T, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://127.0.0.1:5186"+path, nil)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	(&App{}).handler().ServeHTTP(response, request)
	return response
}

func TestFrontendDocumentAndMissingResources(t *testing.T) {
	index := frontendRequest(t, "GET", "/", nil)
	if index.Code != http.StatusOK || index.Header().Get("Cache-Control") != "no-cache" || !strings.Contains(index.Body.String(), `type="module"`) {
		t.Fatalf("invalid application index: %d %v", index.Code, index.Header())
	}
	conditional := frontendRequest(t, "GET", "/settings/statistics", map[string]string{"If-None-Match": index.Header().Get("ETag")})
	if conditional.Code != http.StatusNotModified || conditional.Body.Len() != 0 {
		t.Fatal("application routes do not revalidate their shared HTML")
	}
	head := frontendRequest(t, "HEAD", "/index.html", nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") == "" {
		t.Fatal("application HEAD response is invalid")
	}
	for _, path := range []string{"/assets/", "/assets/missing.js", "/assets/missing.css", "/assets/missing.js.gz", "/app.js", "/vista-web.css", "/unknown.js", "/api/unknown"} {
		response := frontendRequest(t, "GET", path, nil)
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "<!doctype html>") {
			t.Fatalf("missing resource received an application document: %s %d", path, response.Code)
		}
	}
}

func TestFrontendAssetsCachingAndHead(t *testing.T) {
	files, err := fs.Glob(webAssets, "assets/assets/*.js")
	if err != nil || len(files) == 0 {
		t.Fatal("missing application chunks", err)
	}
	styles, _ := fs.Glob(webAssets, "assets/assets/*.css")
	for _, file := range append(files, styles...) {
		path := "/" + strings.TrimPrefix(file, "assets/")
		original, _ := webAssets.ReadFile(file)
		response := frontendRequest(t, "GET", path, map[string]string{"Accept-Encoding": "gzip, deflate, br"})
		if response.Code != http.StatusOK || response.Header().Get("Content-Encoding") != "" || response.Header().Get("Vary") != "" || response.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Fatalf("%s: %d %v", path, response.Code, response.Header())
		}
		if !bytes.Equal(response.Body.Bytes(), original) {
			t.Fatal("asset differs from its uncompressed build output", file)
		}
		conditional := frontendRequest(t, "GET", path, map[string]string{"If-None-Match": response.Header().Get("ETag")})
		if conditional.Code != http.StatusNotModified || conditional.Body.Len() != 0 {
			t.Fatal("asset conditional request did not return 304", file)
		}
		head := frontendRequest(t, "HEAD", path, nil)
		if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != response.Header().Get("Content-Length") || head.Header().Get("Content-Encoding") != "" {
			t.Fatal("asset HEAD differs from GET", file)
		}
	}
}
