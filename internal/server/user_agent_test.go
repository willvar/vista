package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"vista/internal/useragent"
)

func TestUserAgentSettingsAPI(t *testing.T) {
	app := &App{configDir: t.TempDir()}
	if err := os.Chmod(app.configDir, 0700); err != nil {
		t.Fatal(err)
	}
	request := func(method, body string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		app.userAgentSettings(response, httptest.NewRequest(method, "/api/user-agent", strings.NewReader(body)))
		return response
	}
	check := func(want string) {
		t.Helper()
		response := request(http.MethodGet, "")
		var body struct {
			useragent.Config
			Value     string             `json:"value"`
			Presets   []useragent.Preset `json:"presets"`
			MaxLength int                `json:"maxLength"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != 200 || body.Value != want || len(body.Presets) != 9 || body.MaxLength != useragent.MaxLength {
			t.Fatalf("GET: %d %+v %v", response.Code, body, err)
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Error("settings response can be cached")
		}
	}
	check(useragent.Presets()[0].Value)
	for _, preset := range useragent.Presets() {
		response := request(http.MethodPut, `{"preset":"`+preset.ID+`","custom":""}`)
		if response.Code != http.StatusOK {
			t.Fatalf("PUT preset %s: %d %s", preset.ID, response.Code, response.Body.String())
		}
		check(preset.Value)
	}
	if response := request(http.MethodPut, `{"preset":"custom","custom":"Custom/2.0 (test)"}`); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	// A fresh App reads the persisted selection, rather than a process-local cache.
	app = &App{configDir: app.configDir}
	check("Custom/2.0 (test)")
	for _, body := range []string{`{}`, `null`, `{"preset":"missing"}`, `{"preset":"custom","custom":""}`, `{"preset":"custom","custom":"X\r\nInjected: yes"}`, `{"preset":"vista","unknown":true}`, `{"preset":"vista"} {}`, strings.Repeat("x", 4097)} {
		if response := request(http.MethodPut, body); response.Code != http.StatusBadRequest {
			t.Fatalf("accepted invalid settings: %q -> %d", body, response.Code)
		}
		check("Custom/2.0 (test)")
	}
	if response := request(http.MethodPost, ""); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST returned %d", response.Code)
	}
}
