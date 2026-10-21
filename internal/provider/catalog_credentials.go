package provider

import (
	"fmt"
	"os"
)

func resolveCatalogCredential(state *catalogResolveState) error {
	model := &state.config
	if model.APIKey != "" {
		return nil
	}
	for _, name := range state.entry.KeyNames {
		if !catalogCredentialName(name) {
			continue
		}
		if key := os.Getenv(name); key != "" {
			model.APIKey = key
			state.credentialEnv = append(state.credentialEnv, name)
			break
		}
	}
	if model.APIKey == "" {
		return fmt.Errorf("catalog model %q needs apiKey or a configured catalog credential environment variable", model.Key)
	}
	return nil
}
func resolveAnthropicCredential(state *catalogResolveState) error {
	if state.config.AuthToken != "" {
		return nil
	}
	return resolveCatalogCredential(state)
}
func resolveVertexCredential(state *catalogResolveState) error { return nil }
func resolveBedrockCredential(state *catalogResolveState) error {
	if state.config.APIKey == "" && state.config.BedrockAuth != "sigv4" {
		if token := os.Getenv("AWS_BEARER_TOKEN_BEDROCK"); token != "" {
			state.config.APIKey = token
			state.credentialEnv = append(state.credentialEnv, "AWS_BEARER_TOKEN_BEDROCK")
		}
	}
	return nil
}
func resolveSnowflakeCredential(state *catalogResolveState) error {
	if state.config.APIKey == "" {
		if token := os.Getenv("SNOWFLAKE_CORTEX_TOKEN"); token != "" {
			state.config.APIKey = token
			state.credentialEnv = append(state.credentialEnv, "SNOWFLAKE_CORTEX_TOKEN")
		}
	}
	return resolveCatalogCredential(state)
}
func resolveQvacCredential(state *catalogResolveState) error {
	if err := resolveCatalogCredential(state); err != nil {
		state.config.APIKey = "qvac"
	}
	return nil
}
