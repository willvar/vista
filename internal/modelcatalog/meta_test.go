package modelcatalog

import (
	"os"
	"path/filepath"
	"testing"

	"vista/internal/provider"
)

func TestMetaNativeCredentialAliasIsIsolated(t *testing.T) {
	t.Setenv("META_API_KEY", "native-meta-key")
	t.Setenv("META_MODEL_API_KEY", "")
	root := t.TempDir()
	data := `{"default":"meta","models":[{"key":"meta","provider":"catalog","providerId":"meta","id":"muse-spark-1.1"}]}`
	if err := os.WriteFile(filepath.Join(root, "models.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := catalog.Resolve("")
	if err != nil || model.Provider != "openai-responses" || model.APIKey != "native-meta-key" || model.BaseURL != "https://api.meta.ai/v1" {
		t.Fatalf("Meta catalog route: %+v, err=%v", model, err)
	}
	if !provider.IsPinnedCredentialEnvironmentName("META_API_KEY") {
		t.Fatal("native Meta credential was not marked private")
	}
	filtered := WorkerEnvironment([]string{"PATH=/usr/bin", "META_API_KEY=native-meta-key", "META_MODEL_API_KEY=other-key"}, catalog.CredentialEnvironment()...)
	if len(filtered) != 1 || filtered[0] != "PATH=/usr/bin" {
		t.Fatalf("Meta credentials leaked to Worker: %q", filtered)
	}
}
