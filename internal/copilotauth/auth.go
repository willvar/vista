// Package copilotauth implements GitHub's device flow for Copilot accounts.
package copilotauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"vista/internal/useragent"
)

// Public device-flow clients also used by OMP's GitHub Copilot integration.
// https://github.com/can1357/oh-my-pi/blob/602b6c812fa9ef774f359f1e399a09d30ee2eaca/packages/ai/src/registry/oauth/github-copilot.ts
const publicClientID = "Ov23li8tweQw6odWQebz"
const enterpriseClientID = "Ov23ctDVkRmgkPke0Mmm"

type Client struct {
	HTTP     *http.Client
	Issuer   string
	ClientID string
}

// Options are connection-specific device-login settings, separate from the
// discovered Copilot inference endpoint and the account credential.
type Options struct {
	EnterpriseURL string `json:"enterpriseUrl,omitempty"`
	ClientID      string `json:"clientId,omitempty"`
}

func (o Options) Normalize() (Options, error) {
	o.EnterpriseURL = strings.TrimSpace(o.EnterpriseURL)
	o.ClientID = strings.TrimSpace(o.ClientID)
	if o.EnterpriseURL != "" {
		if !strings.Contains(o.EnterpriseURL, "://") {
			o.EnterpriseURL = "https://" + o.EnterpriseURL
		}
		o.EnterpriseURL = strings.TrimRight(o.EnterpriseURL, "/")
		u, err := url.Parse(o.EnterpriseURL)
		if err != nil || len(o.EnterpriseURL) > 2048 || !validURL(o.EnterpriseURL) || u.Path != "" {
			return Options{}, errors.New("enter a GitHub Enterprise domain or HTTPS origin without a path")
		}
	}
	if len(o.ClientID) > 200 || strings.ContainsAny(o.ClientID, "\r\n\t ") {
		return Options{}, errors.New("invalid GitHub OAuth client ID")
	}
	return o, nil
}

type Device struct {
	VerificationURL string                     `json:"verificationUrl"`
	UserCode        string                     `json:"userCode"`
	ExpiresAt       time.Time                  `json:"expiresAt"`
	Challenge       *oauth2.DeviceAuthResponse `json:"-"`
}

type Credential struct{ Token, APIEndpoint string }

func NewClient() *Client {
	return &Client{Issuer: "https://github.com", HTTP: &http.Client{Transport: useragent.Transport{UserAgent: oauthUserAgent}, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("Copilot authorization endpoint redirected")
	}}}
}

func (c *Client) config() (oauth2.Config, error) {
	issuer := strings.TrimRight(c.Issuer, "/")
	u, err := url.Parse(issuer)
	if err != nil || !validURL(issuer) || u.Path != "" {
		return oauth2.Config{}, errors.New("invalid GitHub authorization URL")
	}
	clientID := c.ClientID
	if clientID == "" {
		clientID = publicClientID
		if u.Hostname() != "github.com" && !loopback(u.Hostname()) {
			clientID = enterpriseClientID
		}
	}
	return oauth2.Config{ClientID: clientID, Scopes: []string{"read:user"}, Endpoint: oauth2.Endpoint{DeviceAuthURL: issuer + "/login/device/code", TokenURL: issuer + "/login/oauth/access_token", AuthStyle: oauth2.AuthStyleInParams}}, nil
}

func (c *Client) Begin(ctx context.Context) (Device, error) {
	config, err := c.config()
	if err != nil {
		return Device{}, err
	}
	challenge, err := config.DeviceAuth(context.WithValue(ctx, oauth2.HTTPClient, c.HTTP))
	if err != nil {
		return Device{}, errors.New("could not start GitHub sign-in")
	}
	if !validURL(challenge.VerificationURI) || challenge.UserCode == "" || len(challenge.UserCode) > 100 || challenge.DeviceCode == "" || len(challenge.DeviceCode) > 1024 || challenge.Interval < 0 || challenge.Interval > 60 || time.Until(challenge.Expiry) <= 0 || time.Until(challenge.Expiry) > 30*time.Minute {
		return Device{}, errors.New("invalid GitHub device challenge")
	}
	return Device{VerificationURL: challenge.VerificationURI, UserCode: challenge.UserCode, ExpiresAt: challenge.Expiry, Challenge: challenge}, nil
}

func (c *Client) Complete(ctx context.Context, device Device) (Credential, error) {
	config, err := c.config()
	if err != nil {
		return Credential{}, err
	}
	ctx, cancel := context.WithDeadline(ctx, device.ExpiresAt)
	defer cancel()
	if device.Challenge == nil {
		return Credential{}, errors.New("missing GitHub device challenge")
	}
	challenge := *device.Challenge
	if challenge.Interval > 0 {
		challenge.Interval += 3
	}
	token, err := config.DeviceAccessToken(context.WithValue(ctx, oauth2.HTTPClient, c.HTTP), &challenge)
	if err != nil {
		if ctx.Err() != nil {
			return Credential{}, ctx.Err()
		}
		return Credential{}, errors.New("GitHub sign-in was not completed; start again")
	}
	if token.AccessToken == "" || len(token.AccessToken) > 4096 {
		return Credential{}, errors.New("invalid GitHub access token")
	}
	return c.account(ctx, token.AccessToken)
}

func (c *Client) account(ctx context.Context, token string) (Credential, error) {
	u, _ := url.Parse(c.Issuer)
	api := u.Scheme + "://api." + u.Host
	base := "https://api.githubcopilot.com"
	if u.Hostname() != "github.com" {
		base = "https://copilot-api." + u.Hostname()
	}
	if loopback(u.Hostname()) {
		api = c.Issuer
		base = c.Issuer
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/copilot_internal/user", nil)
	if err != nil {
		return Credential{}, errors.New("invalid GitHub account endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2025-04-01")
	SetAPIHeaders(req.Header)
	response, err := c.HTTP.Do(req)
	if err != nil {
		return Credential{}, errors.New("could not check Copilot account")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Credential{}, errors.New("GitHub account could not access Copilot")
	}
	var account struct {
		ChatEnabled *bool `json:"chat_enabled"`
		Endpoints   struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (16<<10)+1))
	if err != nil || len(raw) > 16<<10 || json.Unmarshal(raw, &account) != nil {
		return Credential{}, errors.New("invalid Copilot account response")
	}
	if account.ChatEnabled != nil && !*account.ChatEnabled {
		return Credential{}, errors.New("GitHub account does not have Copilot chat access")
	}
	if account.Endpoints.API != "" {
		base = strings.TrimRight(account.Endpoints.API, "/")
	}
	if !validURL(base) {
		return Credential{}, errors.New("invalid Copilot API endpoint")
	}
	return Credential{Token: token, APIEndpoint: base}, nil
}

func loopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}
func validURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Scheme == "https" || u.Scheme == "http" && loopback(u.Hostname()))
}
