package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/useragent"
)

var sapAIAuthClient = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
	return errors.New("SAP AI Core endpoint redirected")
}}

type sapAIServiceKey struct {
	URL          string `json:"url"`
	ClientID     string `json:"clientid"`
	ClientSecret string `json:"clientsecret"`
	ServiceURLs  struct {
		AIAPIURL string `json:"AI_API_URL"`
	} `json:"serviceurls"`
	UAA struct {
		URL          string `json:"url"`
		ClientID     string `json:"clientid"`
		ClientSecret string `json:"clientsecret"`
	} `json:"uaa"`
}

// The SAP service key stays in the Server; the Worker receives only the run's
// bearer token, deployment ID, and API endpoint.
func resolveSAPAIForRun(ctx context.Context, model modelcfg.Model, selected string) (modelcfg.Model, string, error) {
	var key sapAIServiceKey
	if json.Unmarshal([]byte(model.APIKey), &key) != nil {
		return modelcfg.Model{}, "", errors.New("invalid SAP AI Core service key")
	}
	authURL, clientID, clientSecret := key.URL, key.ClientID, key.ClientSecret
	if key.UAA.URL != "" {
		authURL, clientID, clientSecret = key.UAA.URL, key.UAA.ClientID, key.UAA.ClientSecret
	}
	if !ValidProviderBaseURL(authURL) || clientID == "" || clientSecret == "" {
		return modelcfg.Model{}, "", errors.New("SAP AI Core service key needs an auth URL and client credentials")
	}
	if model.BaseURL == "" {
		model.BaseURL = key.ServiceURLs.AIAPIURL
	}
	if !ValidProviderBaseURL(model.BaseURL) {
		return modelcfg.Model{}, "", errors.New("SAP AI Core needs a valid API base URL")
	}
	parsed, _ := url.Parse(model.BaseURL)
	if parsed.Path == "" || parsed.Path == "/" {
		model.BaseURL = strings.TrimRight(model.BaseURL, "/") + "/v2"
	} else {
		model.BaseURL = strings.TrimRight(model.BaseURL, "/")
	}
	tokenURL := strings.TrimRight(authURL, "/")
	if !strings.HasSuffix(tokenURL, "/oauth/token") {
		tokenURL += "/oauth/token"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader("grant_type=client_credentials"))
	if err != nil {
		return modelcfg.Model{}, "", errors.New("invalid SAP AI Core auth URL")
	}
	request.SetBasicAuth(clientID, clientSecret)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := sapAIAuthClient.Do(request)
	if err != nil {
		return modelcfg.Model{}, "", errors.New("SAP AI Core token request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return modelcfg.Model{}, "", errors.New("SAP AI Core token request was rejected")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (16<<10)+1))
	if err != nil || len(raw) > 16<<10 {
		return modelcfg.Model{}, "", errors.New("invalid SAP AI Core token response")
	}
	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if json.Unmarshal(raw, &token) != nil || token.AccessToken == "" || len(token.AccessToken) > 4096 {
		return modelcfg.Model{}, "", errors.New("invalid SAP AI Core token response")
	}
	model.APIKey = token.AccessToken
	if token.ExpiresIn > 0 {
		model.CredentialExpiresAt = time.Now().Add(time.Duration(min(token.ExpiresIn, 86400)) * time.Second)
	}
	if model.DeploymentID == "" {
		deployment, err := discoverSAPAIOrchestrationDeployment(ctx, model)
		if err != nil {
			return modelcfg.Model{}, "", err
		}
		model.DeploymentID = deployment
	}
	return model, selected, nil
}

func discoverSAPAIOrchestrationDeployment(ctx context.Context, model modelcfg.Model) (string, error) {
	endpoint := model.BaseURL + "/lm/deployments?scenarioId=orchestration&status=RUNNING"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", errors.New("invalid SAP AI Core deployment endpoint")
	}
	request.Header.Set("Authorization", "Bearer "+model.APIKey)
	request.Header.Set("AI-Resource-Group", model.ResourceGroup)
	response, err := useragent.Do(sapAIAuthClient, request)
	if err != nil {
		return "", errors.New("SAP AI Core deployment discovery failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", errors.New("SAP AI Core deployment discovery was rejected")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return "", errors.New("invalid SAP AI Core deployment response")
	}
	var result struct {
		Resources []struct {
			ID string `json:"id"`
		} `json:"resources"`
	}
	if json.Unmarshal(raw, &result) != nil || len(result.Resources) == 0 || !validCatalogPathSegment(result.Resources[0].ID) {
		return "", errors.New("no running SAP AI Core orchestration deployment")
	}
	return result.Resources[0].ID, nil
}
