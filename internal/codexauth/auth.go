// Package codexauth owns Vista's ChatGPT device login and rotating credentials.
package codexauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Public native-app client used by Codex's ChatGPT authorization flow.
const clientID = "app_EMoamEEZ73f0CkXaXp7hrann"
const LoginLifetime = 15 * time.Minute

type Client struct {
	HTTP   *http.Client
	Issuer string
}

func NewClient() *Client {
	return &Client{Issuer: "https://auth.openai.com", HTTP: &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("Codex authorization endpoint redirected")
		},
	}}
}

type Device struct {
	VerificationURL string    `json:"verificationUrl"`
	UserCode        string    `json:"userCode"`
	ExpiresAt       time.Time `json:"expiresAt"`
	Interval        int       `json:"interval"`
	ID              string    `json:"-"`
}

type Credential struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	AccountID    string    `json:"accountId"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

// Begin and Complete follow Codex's device-code flow. Authorization codes and
// PKCE verifiers stay on the server; the browser receives only the user code.
func (c *Client) Begin(ctx context.Context) (Device, error) {
	var result struct {
		ID       string          `json:"device_auth_id"`
		Code     string          `json:"user_code"`
		Interval json.RawMessage `json:"interval"`
	}
	status, err := c.postJSON(ctx, "/api/accounts/deviceauth/usercode", map[string]string{"client_id": clientID}, &result)
	if err != nil {
		return Device{}, err
	}
	if status != http.StatusOK {
		return Device{}, fmt.Errorf("Codex device login unavailable (HTTP %d); check device-code login in ChatGPT security settings", status)
	}
	if result.ID == "" || result.Code == "" || len(result.ID) > 4096 || len(result.Code) > 100 {
		return Device{}, errors.New("invalid Codex device login response")
	}
	interval, _ := strconv.Atoi(strings.Trim(string(result.Interval), "\" "))
	if interval <= 0 {
		interval = 5
	}
	if interval > 60 {
		return Device{}, errors.New("invalid Codex login polling interval")
	}
	return Device{ID: result.ID, VerificationURL: c.Issuer + "/codex/device", UserCode: result.Code, Interval: interval, ExpiresAt: time.Now().Add(LoginLifetime)}, nil
}

func (c *Client) Complete(ctx context.Context, device Device) (Credential, error) {
	ctx, cancel := context.WithDeadline(ctx, device.ExpiresAt)
	defer cancel()
	for {
		var code struct {
			Code     string `json:"authorization_code"`
			Verifier string `json:"code_verifier"`
		}
		status, err := c.postJSON(ctx, "/api/accounts/deviceauth/token", map[string]string{"device_auth_id": device.ID, "user_code": device.UserCode}, &code)
		if err != nil {
			return Credential{}, err
		}
		if status == http.StatusOK {
			if code.Code == "" || code.Verifier == "" {
				return Credential{}, errors.New("invalid Codex authorization response")
			}
			return c.exchange(ctx, url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code.Code}, "code_verifier": {code.Verifier}, "redirect_uri": {c.Issuer + "/deviceauth/callback"}}, "")
		}
		if status != http.StatusForbidden && status != http.StatusNotFound {
			return Credential{}, fmt.Errorf("Codex authorization failed (HTTP %d); start login again", status)
		}
		timer := time.NewTimer(time.Duration(max(device.Interval, 1)) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Credential{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *Client) postJSON(ctx context.Context, path string, input, output any) (int, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return 0, err
	}
	return c.request(ctx, path, "application/json", bytes.NewReader(data), output)
}

func (c *Client) request(ctx context.Context, path, contentType string, body io.Reader, output any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Issuer+path, body)
	if err != nil {
		return 0, errors.New("invalid Codex authorization endpoint")
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "vista")
	res, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, errors.New("could not reach Codex authorization service; retry login")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return res.StatusCode, nil
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 || json.Unmarshal(data, output) != nil {
		return 0, errors.New("invalid Codex authorization response")
	}
	return res.StatusCode, nil
}

func (c *Client) exchange(ctx context.Context, form url.Values, previousRefresh string) (Credential, error) {
	var result struct {
		Access    string `json:"access_token"`
		Refresh   string `json:"refresh_token"`
		ExpiresIn int64  `json:"expires_in"`
	}
	status, err := c.request(ctx, "/oauth/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), &result)
	if err != nil {
		return Credential{}, err
	}
	if status != http.StatusOK {
		if status == 400 || status == 401 || status == 403 {
			return Credential{}, errors.New("Codex login expired or was revoked; reconnect OpenAI Codex in Settings")
		}
		return Credential{}, fmt.Errorf("Codex token service returned HTTP %d; retry later", status)
	}
	var claims struct {
		Expires int64 `json:"exp"`
		Auth    struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	parts := strings.Split(result.Access, ".")
	if len(parts) != 3 {
		return Credential{}, errors.New("invalid Codex access token")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(data, &claims) != nil || claims.Auth.AccountID == "" {
		return Credential{}, errors.New("Codex login did not return a ChatGPT account")
	}
	expires := time.Unix(claims.Expires, 0)
	if result.ExpiresIn > 0 {
		fromResponse := time.Now().Add(time.Duration(min(result.ExpiresIn, 30*24*3600)) * time.Second)
		if claims.Expires == 0 || fromResponse.Before(expires) {
			expires = fromResponse
		}
	}
	if !expires.After(time.Now()) {
		return Credential{}, errors.New("Codex returned an expired access token")
	}
	if result.Refresh == "" {
		result.Refresh = previousRefresh
	}
	credential := Credential{AccessToken: result.Access, RefreshToken: result.Refresh, AccountID: claims.Auth.AccountID, ExpiresAt: expires}
	if !credential.valid() {
		return Credential{}, errors.New("invalid Codex credential")
	}
	return credential, nil
}
