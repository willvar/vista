package modelcatalog

import (
	"context"
	"net/http"
	"os"
	"reflect"
	"testing"

	modelcfg "vista/internal/model"
)

func TestPromptCachingSettingsPersistResolveAndPreserveOrdinaryEdits(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	key := false
	options := &modelcfg.PromptCaching{Format: "openai", Key: &key, Retention: "24h"}
	input := ConnectionInput{Provider: "openai-responses", ProviderID: "custom", Name: "Cache gateway", BaseURL: "http://127.0.0.1:1/v1", CredentialMode: "none", IDs: []string{"opaque"}, PromptCaching: options}
	raw, err := ChangeConnection(context.Background(), Catalog{}, false, http.MethodPost, input)
	if err != nil {
		t.Fatal(err)
	}
	if !Editable(raw.Models[0]) {
		t.Fatal("cache options made the existing connection uneditable")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	resolved, err := Save(dir, raw)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := resolved.ResolveForRun(context.Background(), raw.Models[0].Key)
	if err != nil || !reflect.DeepEqual(model.PromptCaching, options) || model.ResponsesRoute != "" {
		t.Fatalf("resolution lost options or enabled routing: %+v %v", model.PromptCaching, err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Models[0].PromptCaching, options) {
		t.Fatal("saved cache options were lost")
	}
	input.Key = raw.Models[0].Key
	input.PromptCaching = nil
	input.Name = "Renamed"
	edited, err := ChangeConnection(context.Background(), raw, true, http.MethodPut, input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(edited.Models[0].PromptCaching, options) {
		t.Fatal("ordinary edit dropped capability settings")
	}
	input.BaseURL = "http://127.0.0.1:2/v1"
	moved, err := ChangeConnection(context.Background(), edited, true, http.MethodPut, input)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Models[0].PromptCaching != nil {
		t.Fatal("capabilities silently followed an edited endpoint")
	}
	input.BaseURL = "http://127.0.0.1:1/v1"
	input.PromptCaching = &modelcfg.PromptCaching{Format: "none"}
	disabled, err := ChangeConnection(context.Background(), edited, true, http.MethodPut, input)
	if err != nil || disabled.Models[0].PromptCaching.Format != "none" {
		t.Fatalf("explicit disable lost: %v", err)
	}
	input.Provider, input.PromptCaching = "openai-chat", nil
	switched, err := ChangeConnection(context.Background(), edited, true, http.MethodPut, input)
	if err != nil || switched.Models[0].PromptCaching != nil {
		t.Fatalf("capabilities silently followed a changed protocol: %v", err)
	}
}

func TestPromptCachingRejectsContradictoryConnectionOptions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, tc := range []struct {
		protocol string
		options  modelcfg.PromptCaching
	}{
		{"gemini", modelcfg.PromptCaching{Format: "anthropic"}},
		{"openai-chat", modelcfg.PromptCaching{Format: "bedrock"}},
		{"openai-responses", modelcfg.PromptCaching{TTL: "1h"}},
		{"cohere", modelcfg.PromptCaching{Key: new(bool)}},
		{"openai-chat", modelcfg.PromptCaching{Format: "made-up"}},
		{"gemini", modelcfg.PromptCaching{CachedContent: &modelcfg.CachedContent{Name: "cachedContents/missing-binding"}}},
	} {
		input := ConnectionInput{Provider: tc.protocol, ProviderID: "custom", Name: "Invalid cache", BaseURL: "http://127.0.0.1:1/v1", CredentialMode: "none", IDs: []string{"fixture"}, PromptCaching: &tc.options}
		// False key overrides are meaningful only for key-capable wire formats too.
		raw, err := ChangeConnection(context.Background(), Catalog{}, false, http.MethodPost, input)
		if err == nil {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			_, err = Save(dir, raw)
		}
		if err == nil {
			t.Fatalf("accepted invalid %s cache options: %+v", tc.protocol, tc.options)
		}
	}
}
