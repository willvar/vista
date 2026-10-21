package codexauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"vista/internal/useragent"
)

func testToken(account, version string) string {
	data, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "version": version, "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": account}})
	return "header." + base64.RawURLEncoding.EncodeToString(data) + ".signature"
}

func TestDeviceLoginAndPrivateCredential(t *testing.T) {
	wantAgent := useragent.Codex()
	for _, interval := range []string{`"1"`, `1`} {
		t.Run(interval, func(t *testing.T) {
			var polls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.UserAgent() != wantAgent || r.Header.Get("originator") != useragent.CodexOriginator {
					t.Errorf("sent UA %q, want %q", r.UserAgent(), wantAgent)
				}
				switch r.URL.Path {
				case "/api/accounts/deviceauth/usercode":
					var input map[string]string
					_ = json.NewDecoder(r.Body).Decode(&input)
					if input["client_id"] != clientID {
						t.Error("missing Codex client ID")
					}
					fmt.Fprintf(w, `{"device_auth_id":"private-device-id","user_code":"ABCD-EFGH","interval":%s}`, interval)
				case "/api/accounts/deviceauth/token":
					var input map[string]string
					_ = json.NewDecoder(r.Body).Decode(&input)
					if input["device_auth_id"] != "private-device-id" || input["user_code"] != "ABCD-EFGH" {
						t.Error("incorrect device association")
					}
					if polls.Add(1) == 1 {
						w.WriteHeader(403)
						return
					}
					fmt.Fprint(w, `{"authorization_code":"code","code_verifier":"verifier"}`)
				case "/oauth/token":
					_ = r.ParseForm()
					if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "code" || r.Form.Get("code_verifier") != "verifier" || !strings.HasSuffix(r.Form.Get("redirect_uri"), "/deviceauth/callback") {
						t.Error("incorrect PKCE exchange")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"access_token": testToken("account", "initial"), "refresh_token": "private-refresh", "expires_in": 3600})
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer upstream.Close()
			c := &Client{Issuer: upstream.URL, HTTP: upstream.Client()}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			device, err := c.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			public, _ := json.Marshal(device)
			if strings.Contains(string(public), "private-device-id") {
				t.Fatal("device credential leaked")
			}
			credential, err := c.Complete(ctx, device)
			if err != nil {
				t.Fatal(err)
			}
			path := Path(t.TempDir(), uuid.NewString())
			if err := Save(ctx, path, credential); err != nil {
				t.Fatal(err)
			}
			info, _ := os.Stat(path)
			if info.Mode().Perm() != 0600 {
				t.Fatal("credential is not private")
			}
			loaded, err := Read(ctx, path)
			if err != nil || loaded.AccessToken != credential.AccessToken || loaded.RefreshToken != credential.RefreshToken {
				t.Fatalf("login did not survive reload: %v", err)
			}
		})
	}
}

func TestConcurrentRefreshAndRejectedToken(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path != "/oauth/token" || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("client_id") != clientID {
			t.Error("invalid refresh request")
		}
		count := calls.Add(1)
		if r.Form.Get("refresh_token") != fmt.Sprintf("refresh-%d", count-1) {
			t.Error("stale refresh token was reused")
		}
		time.Sleep(30 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": testToken("account", fmt.Sprint(count)), "refresh_token": fmt.Sprintf("refresh-%d", count), "expires_in": 3600})
	}))
	defer upstream.Close()
	c := &Client{Issuer: upstream.URL, HTTP: upstream.Client()}
	ctx := context.Background()
	path := Path(t.TempDir(), uuid.NewString())
	if err := Save(ctx, path, Credential{AccessToken: testToken("account", "0"), RefreshToken: "refresh-0", AccountID: "account", ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	check := func(rejected string) {
		var group sync.WaitGroup
		for range 8 {
			group.Go(func() {
				if _, err := c.Token(ctx, path, rejected); err != nil {
					t.Error(err)
				}
			})
		}
		group.Wait()
	}
	check("")
	if calls.Load() != 1 {
		t.Fatalf("concurrent requests refreshed %d times", calls.Load())
	}
	credential, _ := Read(ctx, path)
	check(credential.AccessToken)
	if calls.Load() != 2 {
		t.Fatalf("concurrent 401 responses refreshed %d times", calls.Load())
	}
	if err := Remove(ctx, path); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Token(ctx, path, ""); err == nil {
		t.Fatal("disconnected account still usable")
	}
}

func TestRefreshFailureAndCancellation(t *testing.T) {
	ctx := context.Background()
	path := Path(t.TempDir(), uuid.NewString())
	credential := Credential{AccessToken: testToken("account", "0"), RefreshToken: "refresh-secret", AccountID: "account", ExpiresAt: time.Now().Add(-time.Hour)}
	if err := Save(ctx, path, credential); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, "refresh-secret echoed by upstream")
	}))
	defer upstream.Close()
	c := &Client{Issuer: upstream.URL, HTTP: upstream.Client()}
	_, err := c.Token(ctx, path, "")
	if err == nil || !strings.Contains(err.Error(), "reconnect") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("incorrect refresh failure: %v", err)
	}
	stored, _ := Read(ctx, path)
	if stored.RefreshToken != credential.RefreshToken {
		t.Fatal("failed refresh destroyed credentials")
	}
	guard, err := lock(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	wait, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if _, err := c.Token(wait, path, ""); err != context.DeadlineExceeded {
		t.Fatalf("file lock ignored cancellation: %v", err)
	}
	if Path(t.TempDir(), "../outside") != "" {
		t.Fatal("accepted invalid connection ID")
	}
}
