package modelcatalog

import (
	"strings"
	"vista/internal/provider"
)

func WorkerEnvironment(source []string, credentialNames ...string) []string {
	configured := make(map[string]bool, len(credentialNames))
	for _, name := range credentialNames {
		configured[name] = true
	}
	filtered := make([]string, 0, len(source))
	for _, entry := range source {
		name, _, _ := strings.Cut(entry, "=")
		if configured[name] || provider.IsPinnedCredentialEnvironmentName(name) {
			continue
		}
		if strings.HasPrefix(name, "VISTA_") && strings.HasSuffix(name, "_API_KEY") {
			continue
		}
		switch name {
		case "EXA_API_KEY", "FIRECRAWL_API_KEY", "PARALLEL_API_KEY", "TAVILY_API_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_BEARER_TOKEN_BEDROCK", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_SHARED_CREDENTIALS_FILE", "AWS_CONFIG_FILE", "AWS_CONTAINER_AUTHORIZATION_TOKEN", "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_VERTEX_API_KEY", "SNOWFLAKE_CORTEX_TOKEN":
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}
