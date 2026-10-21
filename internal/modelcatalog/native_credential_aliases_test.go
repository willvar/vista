package modelcatalog

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"vista/internal/provider"
)

func TestNativeCredentialAliases(t *testing.T) {
	for _, check := range []struct {
		providerID string
		modelID    string
		pinnedEnv  string
		nativeEnv  string
		protocol   string
		baseURL    string
	}{
		{"moonshotai", "kimi-k2.7-code", "MOONSHOT_API_KEY", "MOONSHOTAI_API_KEY", "openai-chat", "https://api.moonshot.ai/v1"},
		{"moonshotai-cn", "kimi-k2.7-code", "MOONSHOT_API_KEY", "MOONSHOTAI_API_KEY", "openai-chat", "https://api.moonshot.cn/v1"},
		{"zai", "glm-5", "ZHIPU_API_KEY", "ZAI_API_KEY", "openai-chat", "https://api.z.ai/api/paas/v4"},
		{"zai-coding-plan", "glm-5.2", "ZHIPU_API_KEY", "ZAI_API_KEY", "openai-chat", "https://api.z.ai/api/coding/paas/v4"},
		{"zhipuai-coding-plan", "glm-5.3", "ZHIPU_API_KEY", "ZAI_API_KEY", "openai-chat", "https://open.bigmodel.cn/api/coding/paas/v4"},
	} {
		t.Run(check.providerID, func(t *testing.T) {
			t.Setenv(check.pinnedEnv, "")
			t.Setenv(check.nativeEnv, "native-key")
			root := t.TempDir()
			data := fmt.Sprintf(`{"default":"native","models":[{"key":"native","provider":"catalog","providerId":%q,"id":%q}]}`, check.providerID, check.modelID)
			if err := os.WriteFile(filepath.Join(root, "models.json"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			catalog, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			model, _, err := catalog.Resolve("")
			if err != nil || model.Provider != check.protocol || model.BaseURL != check.baseURL || model.APIKey != "native-key" {
				t.Fatalf("native catalog credential: %+v, err=%v", model, err)
			}
			if !provider.IsPinnedCredentialEnvironmentName(check.nativeEnv) {
				t.Fatal("native credential alias is not private")
			}
			filtered := WorkerEnvironment([]string{"PATH=/usr/bin", check.pinnedEnv + "=pinned-key", check.nativeEnv + "=native-key"}, catalog.CredentialEnvironment()...)
			if len(filtered) != 1 || filtered[0] != "PATH=/usr/bin" {
				t.Fatalf("Provider credentials leaked to Worker: %q", filtered)
			}
		})
	}
}
