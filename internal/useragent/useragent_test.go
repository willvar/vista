package useragent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"vista/internal/appdirs"
	"vista/internal/platform"
)

func TestVistaIdentity(t *testing.T) {
	for _, test := range []struct{ version, os, arch, want string }{
		{"v0.1.0", "linux", "amd64", "vista/0.1.0 (Linux; x86_64)"},
		{"dev", "darwin", "arm64", "vista/dev (macOS; arm64)"},
		{"v0.2.0-rc.1", "windows", "amd64", "vista/0.2.0-rc.1 (Windows; x86_64)"},
		{"", "linux", "arm64", "vista/dev (Linux; arm64)"},
	} {
		if got := vista(test.version, test.os, test.arch); got != test.want {
			t.Errorf("vista(%q, %q, %q) = %q, want %q", test.version, test.os, test.arch, got, test.want)
		}
	}
	for _, preset := range Presets() {
		if err := (Config{Preset: "custom", Custom: preset.Value}).Validate(); err != nil {
			t.Errorf("invalid bundled UA %s: %v", preset.ID, err)
		}
	}
}

func TestSettingsPersistenceAndValidation(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	initial, err := Read(dir)
	if err != nil || initial.Preset != "vista" {
		t.Fatalf("missing settings: %+v, %v", initial, err)
	}
	for _, preset := range append(Presets(), Preset{ID: "custom"}) {
		config := Config{Preset: preset.ID, Custom: " MyClient/1.2 (+https://example.com) "}
		if err := Write(dir, config); err != nil {
			t.Fatal(err)
		}
		got, err := Read(dir)
		if err != nil || got.Preset != config.Preset || got.Custom != strings.TrimSpace(config.Custom) || got.Value() != config.Value() {
			t.Fatalf("round trip: %+v, %v", got, err)
		}
	}
	info, err := os.Stat(filepath.Join(dir, Filename))
	if err != nil || !platform.PrivateMode(info.Mode()) {
		t.Fatalf("settings must be private: %v", err)
	}
	for _, bad := range []Config{
		{Preset: "unknown"}, {}, {Preset: "custom"}, {Preset: "custom", Custom: "  "},
		{Preset: "custom", Custom: "Client/1\r\nAuthorization: secret"},
		{Preset: "custom", Custom: "Client/1\t"}, {Preset: "custom", Custom: "中文"},
		{Preset: "custom", Custom: strings.Repeat("x", MaxLength+1)},
	} {
		if err := Write(dir, bad); err == nil {
			t.Errorf("accepted invalid settings: %+v", bad)
		}
	}
	got, err := Read(dir)
	if err != nil || got.Preset != "custom" || got.Value() != "MyClient/1.2 (+https://example.com)" {
		t.Fatalf("rejected writes changed configuration: %+v, %v", got, err)
	}
	for _, data := range []string{`{`, `{}`, `{"preset":"missing"}`, `{"preset":"vista","extra":true}`, `{"preset":"vista"} {}`, `null`} {
		if err := os.WriteFile(filepath.Join(dir, Filename), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(dir); err == nil {
			t.Errorf("accepted invalid file: %s", data)
		}
	}
}

func TestHTTPHotReloadAndOverride(t *testing.T) {
	root := t.TempDir()
	switch runtime.GOOS {
	case "linux":
		t.Setenv("XDG_CONFIG_HOME", root)
	case "windows":
		t.Setenv("AppData", root)
	default:
		t.Skip("isolated config override is tested on Linux and Windows")
	}
	dir, err := appdirs.Config()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	agents := make(chan string, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agents <- r.UserAgent()
		if r.Header.Get("Authorization") != "Bearer retained" || r.Header.Get("X-Session-ID") != "session" {
			t.Error("UA selection modified other headers")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer endpoint.Close()
	client := &http.Client{Transport: Transport{Base: endpoint.Client().Transport}}
	request, err := http.NewRequest(http.MethodGet, endpoint.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer retained")
	request.Header.Set("X-Session-ID", "session")
	send := func(want string, direct bool) {
		t.Helper()
		var response *http.Response
		var err error
		if direct {
			response, err = client.Do(request)
		} else {
			response, err = Do(client, request)
		}
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if seen := <-agents; seen != want {
			t.Fatalf("sent UA %q, want %q", seen, want)
		}
	}
	send(Presets()[0].Value, false)
	for _, custom := range []string{"ClientOne/1", "ClientTwo/2"} {
		if err := Write(dir, Config{Preset: "custom", Custom: custom}); err != nil {
			t.Fatal(err)
		}
		send(custom, false)
		send(custom, true) // Covers requests made by SDKs through the transport.
	}
	if request.UserAgent() != "" {
		t.Fatal("the caller's reusable request was mutated")
	}
	request.Header.Set("User-Agent", "ProviderOverride/1")
	send("ProviderOverride/1", false)
	request.Header.Set("User-Agent", "")
	send("", false)

	// Channel-owned clients do not depend on the configurable default, even
	// when its settings cannot be read. Explicit SDK headers still win.
	if err := os.WriteFile(filepath.Join(dir, Filename), []byte(`{"preset":`), 0600); err != nil {
		t.Fatal(err)
	}
	request.Header.Del("User-Agent")
	client.Transport = Transport{UserAgent: "OAuthClient/1"}
	send("OAuthClient/1", true)
	request.Header.Set("User-Agent", "SDKIdentity/2")
	send("SDKIdentity/2", true)
}
