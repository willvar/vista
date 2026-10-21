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
)

var watsonxIAMEndpoint = "https://iam.cloud.ibm.com/identity/token"

var watsonxIAMClient = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
	return errors.New("IBM IAM endpoint redirected")
}}

// Exchange the long-lived API key in the Server. Workers receive only the
// access token; the credential cache renews it before expiry.
func resolveWatsonxForRun(ctx context.Context, model modelcfg.Model, selected string) (modelcfg.Model, string, error) {
	if model.APIKey == "" || model.ProjectID == "" {
		return modelcfg.Model{}, "", errors.New("watsonx API key and project ID are required")
	}
	values := url.Values{"grant_type": {"urn:ibm:params:oauth:grant-type:apikey"}, "apikey": {model.APIKey}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, watsonxIAMEndpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return modelcfg.Model{}, "", errors.New("invalid IBM IAM endpoint")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := watsonxIAMClient.Do(request)
	if err != nil {
		return modelcfg.Model{}, "", errors.New("IBM IAM token request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return modelcfg.Model{}, "", errors.New("IBM IAM token request was rejected")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (16<<10)+1))
	if err != nil || len(raw) > 16<<10 {
		return modelcfg.Model{}, "", errors.New("invalid IBM IAM token response")
	}
	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if json.Unmarshal(raw, &token) != nil || token.AccessToken == "" || len(token.AccessToken) > 4096 {
		return modelcfg.Model{}, "", errors.New("invalid IBM IAM token response")
	}
	model.APIKey = token.AccessToken
	if token.ExpiresIn > 0 {
		model.CredentialExpiresAt = time.Now().Add(time.Duration(min(token.ExpiresIn, 86400)) * time.Second)
	}
	return model, selected, nil
}
