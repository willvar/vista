package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"vista/internal/platform"
)

type oauthCredential struct {
	Generation string        `json:"generation,omitempty"`
	OriginPath string        `json:"originPath,omitempty"`
	Endpoint   string        `json:"endpoint"`
	Config     oauth2.Config `json:"config"`
	Token      oauth2.Token  `json:"token"`
}

func oauthPath(dir, id string) string { return filepath.Join(dir, "mcp-oauth", id+".json") }

// OAuthConfigured checks saved credentials without refreshing or exposing them.
func OAuthConfigured(dir string, config Config) bool {
	credential, err := readOAuth(oauthPath(dir, config.ID), config.Endpoint)
	return err == nil && (config.ClientID == "" || config.ClientID == credential.Config.ClientID) &&
		(credential.Token.Valid() || credential.Token.RefreshToken != "")
}

// CommitOAuth publishes credentials from a completed, isolated login attempt.
func CommitOAuth(staging, dir string, config Config) error {
	credential, err := readOAuth(oauthPath(staging, config.ID), config.Endpoint)
	if err != nil {
		return err
	}
	path := oauthPath(dir, config.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	unlock, err := lockOAuth(path)
	if err != nil {
		return err
	}
	defer unlock()
	credential.Generation, credential.OriginPath = uuid.NewString(), ""
	return writeOAuth(path, credential)
}

func RemoveOAuth(dir, id string) error {
	path := oauthPath(dir, id)
	unlock, err := lockOAuth(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unlock()
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func readOAuth(path, endpoint string) (oauthCredential, error) {
	var value oauthCredential
	info, err := os.Lstat(path)
	if err != nil {
		return value, err
	}
	if !info.Mode().IsRegular() || !platform.PrivateMode(info.Mode()) || info.Size() > 16<<10 {
		return value, errors.New("MCP OAuth credential file must be private and at most 16 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return value, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (16<<10)+1))
	if err != nil || len(data) > 16<<10 {
		return value, errors.New("MCP OAuth credential file exceeds 16 KiB")
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return value, errors.New("invalid MCP OAuth credential file")
	}
	if value.Endpoint != endpoint || value.Token.AccessToken == "" || value.Config.ClientID == "" || !oauthURLAllowed(value.Config.Endpoint.TokenURL) {
		return value, errors.New("MCP OAuth credentials do not match endpoint or are incomplete")
	}
	return value, nil
}

func writeOAuth(path string, value oauthCredential) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil || !info.IsDir() || !platform.PrivateMode(info.Mode()) {
		return errors.New("MCP OAuth credential directory must be private")
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > 16<<10 {
		return errors.New("MCP OAuth credentials exceed 16 KiB")
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".oauth-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write(encoded); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

type storedTokenSource struct {
	path     string
	endpoint string
}

func lockOAuth(path string) (func(), error) {
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := platform.TryLock(lock); err == nil {
			break
		} else if !platform.IsLockBusy(err) {
			lock.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			lock.Close()
			return nil, errors.New("MCP OAuth credential lock timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return func() { _ = lock.Close() }, nil
}

func (s *storedTokenSource) Token() (*oauth2.Token, error) {
	unlock, err := lockOAuth(s.path)
	if err != nil {
		return nil, err
	}
	defer unlock()
	credential, err := readOAuth(s.path, s.endpoint)
	if err != nil {
		return nil, err
	}
	previous := credential.Token
	// Snapshots keep old runs authorized after configuration edits. Refresh a
	// current grant under its original file lock and persist rotating tokens,
	// without recreating deleted credentials or overwriting a newer login.
	mirror := false
	if credential.OriginPath != "" && credential.OriginPath != s.path {
		if releaseOrigin, lockErr := lockOAuth(credential.OriginPath); lockErr == nil {
			defer releaseOrigin()
			if current, readErr := readOAuth(credential.OriginPath, s.endpoint); readErr == nil && oauthGeneration(current) == credential.Generation {
				credential.Token = current.Token
				mirror = true
			}
		}
	}
	httpClient := &http.Client{Timeout: 20 * time.Second}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, httpClient)
	token, err := credential.Config.TokenSource(ctx, &credential.Token).Token()
	if err != nil {
		return nil, errors.New("MCP OAuth token refresh failed; reauthorize in WebUI Settings > MCP")
	}
	if token.AccessToken != previous.AccessToken || token.RefreshToken != previous.RefreshToken || !token.Expiry.Equal(previous.Expiry) {
		credential.Token = *token
		if err := writeOAuth(s.path, credential); err != nil {
			return nil, err
		}
		if mirror {
			original := credential
			original.OriginPath = ""
			if err := writeOAuth(credential.OriginPath, original); err != nil {
				return nil, err
			}
		}
	}
	return token, nil
}

type storedOAuthHandler struct {
	name   string
	source oauth2.TokenSource
}

func (h *storedOAuthHandler) TokenSource(context.Context) (oauth2.TokenSource, error) {
	return h.source, nil
}
func (h *storedOAuthHandler) Authorize(_ context.Context, _ *http.Request, response *http.Response) error {
	response.Body.Close()
	return fmt.Errorf("MCP %s needs OAuth login; authorize it in WebUI Settings > MCP", h.name)
}

func oauthURLAllowed(raw string) bool {
	return ValidateConfig(Config{ID: "oauth", Name: "oauth", Transport: "http", Endpoint: raw}) == nil
}

// Login performs the SDK's authorization-code and PKCE flow through a temporary
// loopback callback. openURL presents the authorization URL to the user.
func Login(ctx context.Context, config Config, configDir string, openURL func(string)) error {
	if err := ValidateConfig(config); err != nil {
		return err
	}
	if !config.OAuth || config.Transport != "http" {
		return errors.New("MCP server is not configured for OAuth")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	redirectURL := "http://" + listener.Addr().String() + "/callback"
	callback := make(chan *auth.AuthorizationResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || len(r.URL.RawQuery) > 4096 {
			http.Error(w, "invalid OAuth callback", 400)
			return
		}
		result := &auth.AuthorizationResult{Code: r.URL.Query().Get("code"), State: r.URL.Query().Get("state"), Iss: r.URL.Query().Get("iss")}
		if result.Code == "" || result.State == "" {
			http.Error(w, "OAuth login did not return a code and state", 400)
			return
		}
		select {
		case callback <- result:
			_, _ = io.WriteString(w, "Vista MCP login received. You may close this tab.")
		default:
			http.Error(w, "OAuth callback already received", 409)
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(listener)
	defer server.Close()
	loginCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	path := oauthPath(configDir, config.ID)
	handlerConfig := &auth.AuthorizationCodeHandlerConfig{
		RedirectURL: redirectURL,
		Client:      &http.Client{Timeout: 20 * time.Second},
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			openURL(args.URL)
			select {
			case result := <-callback:
				return result, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		RequestRefreshToken: true,
		NewTokenSource: func(_ context.Context, oauthConfig *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
			value := oauthCredential{Endpoint: config.Endpoint, Config: *oauthConfig, Token: *token}
			if err := writeOAuth(path, value); err != nil {
				return nil, err
			}
			return &storedTokenSource{path: path, endpoint: config.Endpoint}, nil
		},
	}
	if config.ClientID != "" {
		client := &oauthex.ClientCredentials{ClientID: config.ClientID}
		if config.ClientSecret != "" {
			client.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: config.ClientSecret}
		}
		handlerConfig.PreregisteredClient = client
	} else {
		handlerConfig.DynamicClientRegistrationConfig = &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			RedirectURIs: []string{redirectURL}, TokenEndpointAuthMethod: "none", GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"}, ClientName: "Vista",
		}}
	}
	handler, err := auth.NewAuthorizationCodeHandler(handlerConfig)
	if err != nil {
		return err
	}
	connection, err := openWithHandler(loginCtx, config, handler, 5*time.Minute, maxHTTPResponse)
	if err != nil {
		return err
	}
	defer connection.Close()
	_, err = readOAuth(path, config.Endpoint)
	return err
}
