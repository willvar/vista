package provider

import (
	"context"
	"errors"

	modelcfg "vista/internal/model"
	"vista/internal/network"
)

// Resolve refreshable AWS credentials before handing the selected model to a Worker.
func resolveBedrockForRun(ctx context.Context, model modelcfg.Model, selected string) (modelcfg.Model, string, error) {
	if model.APIKey != "" { // Explicit Bedrock bearer token takes precedence.
		return model, selected, nil
	}
	cfg, release, err := bedrockAWSConfig(ctx, model.AWSRegion)
	defer release()
	if err != nil {
		if ctx.Err() != nil {
			return modelcfg.Model{}, "", ctx.Err()
		}
		return modelcfg.Model{}, "", errors.New("Bedrock AWS configuration unavailable")
	}
	if model.BedrockAuth != "sigv4" && cfg.BearerAuthTokenProvider != nil {
		token, err := cfg.BearerAuthTokenProvider.RetrieveBearerToken(ctx)
		if err == nil && token.Value != "" {
			model.APIKey = token.Value
			model.CredentialExpiresAt = token.Expires
			return model, selected, nil
		}
	}
	if model.BedrockAuth == "bearer" {
		return modelcfg.Model{}, "", errors.New("Bedrock bearer token unavailable")
	}
	if cfg.Credentials == nil {
		return modelcfg.Model{}, "", errors.New("Bedrock AWS credentials unavailable")
	}
	credentials, err := cfg.Credentials.Retrieve(ctx)
	if ctx.Err() != nil {
		return modelcfg.Model{}, "", ctx.Err()
	}
	if errors.Is(err, network.ErrRuntimeIdentityProxy) {
		return modelcfg.Model{}, "", network.ErrRuntimeIdentityProxy
	}
	if err != nil || !credentials.HasKeys() {
		return modelcfg.Model{}, "", errors.New("Bedrock AWS credentials unavailable")
	}
	model.AWSAccessID = credentials.AccessKeyID
	model.AWSSecret = credentials.SecretAccessKey
	model.AWSSession = credentials.SessionToken
	if credentials.CanExpire {
		model.CredentialExpiresAt = credentials.Expires
	}
	return model, selected, nil
}
