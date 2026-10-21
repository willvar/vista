package copilotauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"vista/internal/appdirs"
	"vista/internal/useragent"
)

func TestDeviceLoginUsesBuiltInClientAndCanBeCanceled(t *testing.T) {
	wantAgent := oauthUserAgent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != wantAgent {
			t.Errorf("sent UA %q, want %q", r.UserAgent(), wantAgent)
		}
		if r.URL.Path != "/login/device/code" {
			t.Error("canceled login reached token endpoint")
			return
		}
		if r.ParseForm() != nil || r.Form.Get("client_id") != publicClientID {
			t.Error("missing built-in client")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"verification_uri":%q,"user_code":"CODE","device_code":"private-challenge","interval":1,"expires_in":60}`, "http://"+r.Host+"/verify")
	}))
	defer server.Close()
	client := NewClient()
	client.Issuer = server.URL
	device, err := client.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Complete(ctx, device); err != context.Canceled {
		t.Fatalf("canceled device login: %v", err)
	}
	enterprise := NewClient()
	enterprise.Issuer = "https://company.ghe.com"
	config, err := enterprise.config()
	if err != nil || config.ClientID != enterpriseClientID {
		t.Fatal("enterprise client not selected")
	}
}

func TestDeniedOrMalformedCopilotAccountDoesNotReturnCredential(t *testing.T) {
	for _, body := range []string{`{"chat_enabled":false}`, `{"endpoints":{"api":"http://external.example"}}`, `{"token":"secret"`, strings.Repeat("x", (16<<10)+1)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		client := &Client{Issuer: server.URL, HTTP: server.Client()}
		credential, err := client.account(context.Background(), "private-token")
		server.Close()
		if err == nil || credential.Token != "" || strings.Contains(err.Error(), "private-token") {
			t.Fatal("invalid account accepted or exposed credential")
		}
	}
	client := NewClient()
	client.Issuer = "http://remote.example"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := client.Begin(ctx); err == nil {
		t.Fatal("invalid issuer accepted")
	}
}

func TestCompleteLoginKeepsNativeIdentityWhenGlobalSettingsChange(t *testing.T) {
	root := t.TempDir()
	switch runtime.GOOS {
	case "linux":
		t.Setenv("XDG_CONFIG_HOME", root)
	case "windows":
		t.Setenv("AppData", root)
	default:
		t.Skip("isolated config override requires Linux or Windows")
	}
	dir, err := appdirs.Config()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := useragent.Write(dir, useragent.Config{Preset: "custom", Custom: "UnrelatedClient/1"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/copilot_internal/user" {
			if r.UserAgent() != UserAgent || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Editor-Version") != UserAgent {
				t.Errorf("account lookup lost native identity: %v", r.Header)
			}
			fmt.Fprint(w, `{"chat_enabled":true}`)
			return
		}
		if r.UserAgent() != oauthUserAgent {
			t.Errorf("OAuth identity replaced: %q", r.UserAgent())
		}
		switch r.URL.Path {
		case "/login/device/code":
			fmt.Fprintf(w, `{"verification_uri":%q,"user_code":"CODE","device_code":"private-challenge","interval":1,"expires_in":60}`, "http://"+r.Host+"/verify")
		case "/login/oauth/access_token":
			fmt.Fprint(w, `{"access_token":"test-token","token_type":"Bearer"}`)
		default:
			t.Errorf("unexpected login path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewClient()
	client.Issuer = server.URL
	device, err := client.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, useragent.Filename), []byte(`{"preset":`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	credential, err := client.Complete(ctx, device)
	if err != nil || credential.Token != "test-token" {
		t.Fatalf("global UA settings interfered with login: %+v %v", credential, err)
	}
}

func TestLoginOptions(t *testing.T) {
	for _, test := range []struct {
		name     string
		input    Options
		issuer   string
		clientID string
		invalid  bool
	}{
		{name: "default", issuer: "https://github.com", clientID: publicClientID},
		{name: "enterprise domain", input: Options{EnterpriseURL: " company.ghe.com/ "}, issuer: "https://company.ghe.com", clientID: enterpriseClientID},
		{name: "custom app", input: Options{EnterpriseURL: "https://company.ghe.com", ClientID: " custom-app "}, issuer: "https://company.ghe.com", clientID: "custom-app"},
		{name: "public custom app", input: Options{ClientID: "custom-app"}, issuer: "https://github.com", clientID: "custom-app"},
		{name: "invalid path", input: Options{EnterpriseURL: "https://company.ghe.com/login"}, invalid: true},
		{name: "invalid scheme", input: Options{EnterpriseURL: "http://company.ghe.com"}, invalid: true},
		{name: "invalid credentials", input: Options{EnterpriseURL: "https://user:secret@company.ghe.com"}, invalid: true},
		{name: "invalid query", input: Options{EnterpriseURL: "https://company.ghe.com?login=true"}, invalid: true},
		{name: "invalid client", input: Options{ClientID: "app with spaces"}, invalid: true},
		{name: "oversized client", input: Options{ClientID: strings.Repeat("a", 201)}, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			options, err := test.input.Normalize()
			if test.invalid {
				if err == nil {
					t.Fatal("invalid login options accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			client := NewClient()
			if options.EnterpriseURL != "" {
				client.Issuer = options.EnterpriseURL
			}
			client.ClientID = options.ClientID
			config, err := client.config()
			if err != nil || config.ClientID != test.clientID || config.Endpoint.DeviceAuthURL != test.issuer+"/login/device/code" || config.Endpoint.TokenURL != test.issuer+"/login/oauth/access_token" {
				t.Fatalf("incorrect login route: %+v %v", config, err)
			}
		})
	}
}
