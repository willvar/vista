package provider

import (
	"context"
	"errors"

	"golang.org/x/oauth2/google"

	modelcfg "vista/internal/model"
)

func resolveVertexForRun(ctx context.Context, model modelcfg.Model, selected string) (modelcfg.Model, string, error) {
	if model.APIKey != "" { // The model already has an explicit access token or Gemini Express Mode key.
		return model, selected, nil
	}
	source, err := google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return modelcfg.Model{}, "", errors.New("Google Vertex application credentials unavailable")
	}
	token, err := source.Token()
	if err != nil || token == nil || !token.Valid() {
		return modelcfg.Model{}, "", errors.New("Google Vertex access token unavailable")
	}
	model.APIKey = token.AccessToken
	model.CredentialExpiresAt = token.Expiry
	return model, selected, nil
}
