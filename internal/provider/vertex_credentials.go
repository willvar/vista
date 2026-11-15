package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"time"

	"cloud.google.com/go/compute/metadata"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	modelcfg "vista/internal/model"
	"vista/internal/network"
)

var errVertexMetadataRoute = network.ErrRuntimeIdentityProxy

func resolveVertexForRun(ctx context.Context, model modelcfg.Model, selected string) (modelcfg.Model, string, error) {
	if model.APIKey != "" { // The model already has an explicit access token or Gemini Express Mode key.
		return model, selected, nil
	}
	httpClient, err := network.ClientFor(ctx, http.Client{}, network.ConnectionScope)
	if err != nil {
		return modelcfg.Model{}, "", err
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, httpClient)
	token, err := vertexDefaultToken(ctx, httpClient)
	if ctx.Err() != nil {
		return modelcfg.Model{}, "", ctx.Err()
	}
	if errors.Is(err, errVertexMetadataRoute) {
		return modelcfg.Model{}, "", err
	}
	if err != nil || token == nil || !token.Valid() {
		return modelcfg.Model{}, "", errors.New("Google Vertex access token unavailable")
	}
	model.APIKey = token.AccessToken
	model.CredentialExpiresAt = token.Expiry
	return model, selected, nil
}

// oauth2/google v0.35 has no metadata-client hook and uses background contexts
// in ADC's GCE branch. Delegate file credentials to it, and bridge only the GCE
// request through metadata's injected client and context-aware read. Do not use
// OnGCE: its platform probe starts DNS outside Vista's selected network policy.
func vertexDefaultToken(ctx context.Context, client *http.Client) (*oauth2.Token, error) {
	const scope = "https://www.googleapis.com/auth/cloud-platform"
	data, err := vertexADCJSON()
	if err != nil {
		return nil, err
	}
	if data != nil {
		match, err := vertexRuntimeIdentityRequests(data)
		if err != nil {
			return nil, err
		}
		fileClient, err := network.NewRuntimeIdentityClient(ctx, client, match)
		if err != nil {
			return nil, err
		}
		ctx = context.WithValue(ctx, oauth2.HTTPClient, fileClient.Client)
		credentials, err := google.CredentialsFromJSON(ctx, data, scope)
		if err != nil {
			return nil, err
		}
		token, err := credentials.TokenSource.Token()
		if err != nil && fileClient.RouteError() != nil {
			return nil, fileClient.RouteError()
		}
		return token, err
	}
	metadataClient, err := network.NewRuntimeIdentityClient(ctx, client, nil)
	if err != nil {
		return nil, err
	}
	options := *metadataClient.Client
	if options.Timeout == 0 || options.Timeout > 5*time.Second {
		options.Timeout = 5 * time.Second // Preserve metadata's per-request timeout.
	}
	service := metadata.NewClient(&options)
	metadataContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := service.GetWithContext(metadataContext, "instance/service-accounts/default/token?"+url.Values{"scopes": {scope}}.Encode())
	if err != nil {
		return nil, err
	}
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal([]byte(raw), &token); err != nil {
		return nil, err
	}
	if token.AccessToken == "" || token.ExpiresIn <= 0 {
		return nil, errors.New("incomplete GCE access token")
	}
	return &oauth2.Token{AccessToken: token.AccessToken, TokenType: token.TokenType, Expiry: time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)}, nil
}

// Keep Google's ADC precedence and platform paths. An explicit unreadable file
// is an error; an unavailable well-known file allows metadata discovery.
func vertexADCJSON() ([]byte, error) {
	if path := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); path != "" {
		return os.ReadFile(path)
	}
	base := os.Getenv("APPDATA")
	if runtime.GOOS != "windows" {
		home, err := os.UserHomeDir()
		if err != nil {
			if account, err := user.Current(); err == nil {
				home = account.HomeDir
			}
		}
		base = filepath.Join(home, ".config")
	}
	data, err := os.ReadFile(filepath.Join(base, "gcloud", "application_default_credentials.json"))
	if err != nil {
		return nil, nil
	}
	return data, nil
}
