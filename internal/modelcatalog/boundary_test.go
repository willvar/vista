package modelcatalog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	modelcfg "vista/internal/model"
	"vista/internal/modelcatalog"
	"vista/internal/provider"
	"vista/internal/testcatalog"
)

func TestSaveKeepsRawConfigurationSeparateFromResolvedCatalog(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPSEEK_API_KEY", "environment-only-secret")
	raw := modelcatalog.Catalog{Default: "deepseek", Models: []modelcatalog.Configured{{
		Key: "deepseek", Provider: "catalog", ProviderID: "deepseek", ID: "deepseek-flash",
	}}}
	want, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := modelcatalog.Save(dir, raw)
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, "models.json"))
	if err != nil || !bytes.Equal(onDisk, want) {
		t.Fatalf("saved configuration gained resolved fields: %s, %v", onDisk, err)
	}
	unchanged, _ := json.Marshal(raw)
	if !bytes.Equal(unchanged, want) {
		t.Fatal("Save mutated its input configuration")
	}
	model, key, err := resolved.ResolveForRun(context.Background(), "")
	if err != nil || key != raw.Default || model.APIKey != "environment-only-secret" || model.Provider != "openai-chat" || model.Context <= 0 {
		t.Fatalf("resolved model missing route, credentials or capabilities: key=%q err=%v", key, err)
	}
	public := resolved.Public()
	encoded, err := json.Marshal(public)
	if err != nil || bytes.Contains(encoded, []byte("environment-only-secret")) || bytes.Contains(encoded, []byte("apiKey")) || bytes.Contains(encoded, []byte("credentialEnv")) {
		t.Fatalf("public model projection leaked configuration credentials: %s, %v", encoded, err)
	}
	loaded, err := modelcatalog.Load(dir)
	if err != nil || !reflect.DeepEqual(loaded.Public(), public) {
		t.Fatalf("saved model catalog did not round trip: %v", err)
	}
	invalid := raw
	invalid.Default = "missing-model"
	if _, err := modelcatalog.Save(dir, invalid); err == nil {
		t.Fatal("invalid default was persisted")
	}
	after, err := os.ReadFile(filepath.Join(dir, "models.json"))
	if err != nil || !bytes.Equal(after, want) {
		t.Fatal("failed validation changed the existing configuration")
	}
}

func TestCustomConfigurationRetainsCapabilitiesAndReasoningAcrossBoundary(t *testing.T) {
	original := provider.CurrentOMPCatalog()
	t.Cleanup(original.Activate)
	testcatalog.Install(t, provider.LoadOMPCatalog)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw := modelcatalog.Catalog{Default: "custom-luna", Models: []modelcatalog.Configured{{
		Key: "custom-luna", ConnectionID: "connection", Name: "Custom", Provider: "openai-responses",
		ID: "gpt-6-luna", BaseURL: "http://192.0.2.1/v1", APIKey: "explicit-secret",
		InputModalities: []string{"text", "image"},
	}}}
	if !modelcatalog.ValidCustomProviderBaseURL(raw.Models[0].BaseURL) {
		t.Fatal("HTTP custom endpoint is no longer accepted")
	}
	resolved, err := modelcatalog.Save(dir, raw)
	if err != nil {
		t.Fatal(err)
	}
	info := resolved.Public()[0]
	if info.Key != "custom-luna" || info.ConnectionID != "connection" || info.Name != "Custom" || info.BaseURL != raw.Models[0].BaseURL || !slices.Contains(info.InputModalities, "image") || len(info.ReasoningOptions) == 0 {
		t.Fatalf("custom model metadata changed: %+v", info)
	}
	model, key, err := resolved.ResolveForRun(context.Background(), "custom-luna")
	if err != nil || key != info.Key || model.APIKey != "explicit-secret" || model.BaseURL != info.BaseURL || model.Context != info.Context || model.InputLimit != info.InputLimit || model.OutputLimit != info.OutputLimit {
		t.Fatalf("custom runtime resolution changed: key=%q err=%v", key, err)
	}
	effort := info.ReasoningOptions[len(info.ReasoningOptions)-1]
	if err := modelcatalog.ApplyReasoningEffort(&model, effort); err != nil || model.ReasoningEffort != effort {
		t.Fatalf("advertised reasoning option cannot be applied: %v", err)
	}
	if _, _, err := resolved.Resolve("missing-model"); err == nil {
		t.Fatal("unknown model key fell back silently")
	}
	if env := resolved.CredentialEnvironment(); len(env) != 0 {
		t.Fatalf("explicit credential acquired an environment dependency: %v", env)
	}
}

func TestResponsesRouteCrossesCatalogAndWorkerBoundary(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw := modelcatalog.Catalog{Default: "gateway", Models: []modelcatalog.Configured{{
		Key: "gateway", Provider: "openai-responses", ProviderID: "herdex", ID: "gpt-6.1-sol",
		BaseURL: "http://192.0.2.1/v1", APIKey: "fixture-key", ResponsesRoute: modelcfg.ResponsesRouteCodex,
	}}}
	resolved, err := modelcatalog.Save(dir, raw)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := resolved.ResolveForRun(context.Background(), "gateway")
	if err != nil || model.ResponsesRoute != modelcfg.ResponsesRouteCodex {
		t.Fatalf("Responses transport route did not survive resolution: %+v, %v", model, err)
	}
	encoded, err := json.Marshal(resolved)
	if err != nil || !bytes.Contains(encoded, []byte(`"responsesRoute":"codex"`)) {
		t.Fatalf("search metadata leaked into the catalog: %s, %v", encoded, err)
	}
}
