package modelcatalog

import (
	"slices"
	"testing"
)

func TestWorkerEnvironmentOmitsConfiguredProviderSecrets(t *testing.T) {
	input := []string{
		"PATH=/usr/bin", "HOME=/tmp/test", "VISTA_OPENAI_API_KEY=secret", "VISTA_ANTHROPIC_API_KEY=secret",
		"VISTA_OLLAMA_API_KEY=secret", "VISTA_VLLM_API_KEY=secret", "VISTA_WEBSEARCH_EXA_API_KEY=secret", "EXA_API_KEY=secret",
		"VISTA_OPENAI_BASE_URL=http://127.0.0.1:1234/v1", "DEEPSEEK_API_KEY=secret", "ANTHROPIC_API_KEY=unused-secret", "AICORE_SERVICE_KEY=unused-secret", "GITHUB_TOKEN=unused-provider-secret", "SNOWFLAKE_CORTEX_TOKEN=unused-secret", "CUSTOM_TOOL_TOKEN=user-tool-secret", "USER_DEFINED=value",
	}
	got := WorkerEnvironment(input, "DEEPSEEK_API_KEY")
	want := []string{"PATH=/usr/bin", "HOME=/tmp/test", "VISTA_OPENAI_BASE_URL=http://127.0.0.1:1234/v1", "CUSTOM_TOOL_TOKEN=user-tool-secret", "USER_DEFINED=value"}
	if !slices.Equal(got, want) {
		t.Fatalf("Worker environment: %q, want %q", got, want)
	}
}
