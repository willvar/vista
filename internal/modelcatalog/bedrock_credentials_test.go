package modelcatalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBedrockCatalogCredentialsStayInServer(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	data := []byte(`{"default":"bedrock","models":[{"key":"bedrock","provider":"catalog","providerId":"amazon-bedrock","id":"us.anthropic.claude-haiku-4-5-20251001-v1:0","region":"us-east-1"}]}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "test-access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret")
	t.Setenv("AWS_SESSION_TOKEN", "test-session")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := catalog.ResolveForRun(context.Background(), "")
	if err != nil || model.Provider != "bedrock" || model.AWSRegion != "us-east-1" || model.AWSAccessID != "test-access" || model.AWSSecret != "test-secret" || model.AWSSession != "test-session" || model.APIKey != "" {
		t.Fatalf("Bedrock resolved model: %+v, err=%v", model, err)
	}
	public := catalog.Public()
	if len(public) != 1 || public[0].Provider != "bedrock" {
		t.Fatalf("Bedrock public model: %+v", public)
	}
	workerEnv := WorkerEnvironment([]string{"PATH=/usr/bin", "AWS_ACCESS_KEY_ID=test-access", "AWS_SECRET_ACCESS_KEY=test-secret", "AWS_SESSION_TOKEN=test-session", "AWS_BEARER_TOKEN_BEDROCK=private-bearer", "AWS_SHARED_CREDENTIALS_FILE=/tmp/secret", "AWS_CONFIG_FILE=/tmp/config"})
	if len(workerEnv) != 1 || workerEnv[0] != "PATH=/usr/bin" {
		t.Fatalf("AWS credentials reached Worker environment: %q", workerEnv)
	}
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "private-bearer")
	catalog, err = Load(root)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err = catalog.ResolveForRun(context.Background(), "")
	if err != nil || model.APIKey != "private-bearer" || model.AWSAccessID != "" || len(catalog.CredentialEnvironment()) != 1 || catalog.CredentialEnvironment()[0] != "AWS_BEARER_TOKEN_BEDROCK" {
		t.Fatalf("Bedrock bearer precedence: %+v, env=%q, err=%v", model, catalog.CredentialEnvironment(), err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), "us-east-1", "unsafe.example/region", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil {
		t.Fatal("unsafe Bedrock region accepted")
	}
}

func TestBedrockExplicitAuthMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	write := func(auth, apiKey string) error {
		body := `{"default":"bedrock","models":[{"key":"bedrock","provider":"catalog","providerId":"amazon-bedrock","id":"us.anthropic.claude-haiku-4-5-20251001-v1:0","region":"us-east-1","auth":"` + auth + `","apiKey":"` + apiKey + `"}]}`
		return os.WriteFile(path, []byte(body), 0600)
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "test-access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "env-bearer")
	if err := write("sigv4", ""); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := catalog.ResolveForRun(context.Background(), "")
	if err != nil || model.BedrockAuth != "sigv4" || model.APIKey != "" || model.AWSAccessID != "test-access" || model.AWSSecret != "test-secret" {
		t.Fatalf("explicit SigV4 did not ignore environment bearer token: %+v, err=%v", model, err)
	}
	if err := write("bearer", ""); err != nil {
		t.Fatal(err)
	}
	catalog, err = Load(root)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err = catalog.ResolveForRun(context.Background(), "")
	if err != nil || model.BedrockAuth != "bearer" || model.APIKey != "env-bearer" || model.AWSAccessID != "" {
		t.Fatalf("explicit bearer did not use token: %+v, err=%v", model, err)
	}
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "bearer auth requires") {
		t.Fatalf("bearer mode accepted without token: %v", err)
	}
	if err := write("sigv4", "explicit-bearer"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "SigV4 auth does not accept") {
		t.Fatalf("SigV4 mode accepted explicit bearer token: %v", err)
	}
	if err := write("unknown", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "invalid Bedrock auth mode") {
		t.Fatalf("unknown Bedrock auth mode accepted: %v", err)
	}
}

func TestBedrockDefaultRegionFallback(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	data := []byte(`{"default":"bedrock","models":[{"key":"bedrock","provider":"catalog","providerId":"amazon-bedrock","id":"us.anthropic.claude-haiku-4-5-20251001-v1:0","auth":"bearer","apiKey":"test-token"}]}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "eu-west-1")
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := catalog.Resolve("")
	if err != nil || model.AWSRegion != "eu-west-1" {
		t.Fatalf("Bedrock did not use AWS_DEFAULT_REGION: %+v, err=%v", model, err)
	}
}

func TestBedrockThinkingConfiguration(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	write := func(thinking string) error {
		body := `{"default":"bedrock","models":[{"key":"bedrock","provider":"catalog","providerId":"amazon-bedrock","id":"us.anthropic.claude-haiku-4-5-20251001-v1:0","region":"us-east-1","auth":"bearer","apiKey":"test-token","thinking":` + thinking + `}]}`
		return os.WriteFile(path, []byte(body), 0600)
	}
	if err := write(`{"type":"enabled","budgetTokens":4096}`); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := catalog.Resolve("")
	if err != nil || model.Thinking == nil || model.Thinking.BudgetAlias == nil || *model.Thinking.BudgetAlias != 4096 {
		t.Fatalf("Bedrock thinking option was not preserved: %+v, err=%v", model, err)
	}
	for _, invalid := range []string{`{"type":"disabled"}`, `{"type":"enabled","budgetTokens":100}`, `{"type":"enabled","budgetTokens":4096,"keep":"all"}`} {
		if err := write(invalid); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "invalid Bedrock thinking option") {
			t.Fatalf("invalid Bedrock thinking option %s accepted: %v", invalid, err)
		}
	}
}

func TestBedrockReasoningEffortConfiguration(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	write := func(effort string) error {
		body := `{"default":"bedrock","models":[{"key":"bedrock","provider":"catalog","providerId":"amazon-bedrock","id":"us.amazon.nova-2-lite-v1:0","region":"us-east-1","auth":"bearer","apiKey":"test-token","reasoningEffort":"` + effort + `"}]}`
		return os.WriteFile(path, []byte(body), 0600)
	}
	if err := write("high"); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := catalog.Resolve("")
	if err != nil || model.ReasoningEffort != "high" {
		t.Fatalf("Bedrock reasoning effort was not preserved: %+v, err=%v", model, err)
	}
	if err := write("unsafe value"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "invalid Bedrock reasoningEffort") {
		t.Fatalf("invalid Bedrock reasoning effort accepted: %v", err)
	}
}
