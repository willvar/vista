package modelcatalog

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"vista/internal/configstore"
	"vista/internal/model"
)

func TestSpecializedModelsShareSavedConnection(t *testing.T) {
	const ref = "test-gateway-account"
	source := Configured{Key: "old-chat-anchor", ConnectionID: ref, Provider: "openai-responses", ProviderID: "custom", ID: "chat", BaseURL: "https://example.test/gateway", APIKey: "fixture-key", Authentication: "x-api-key", ProxyID: "saved-proxy", Headers: map[string]string{"X-Fixture-Account": "account-a"}}
	catalog := Catalog{Default: source.Key, Models: []Configured{source}}
	for _, edit := range []GenerationEdit{
		{Kind: model.ImageOutput, Adapter: "openai", Model: "image-test", Connection: ref},
		{Kind: model.VideoOutput, Adapter: "xai", Model: "video-test", Connection: ref},
		{Kind: model.DecisionOutput, Model: "judge-test", Connection: ref},
	} {
		var err error
		catalog, err = catalog.EditGeneration(edit)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(catalog.Models) != 4 || len(catalog.Public()) != 1 {
		t.Fatalf("unexpected specialized/chat count")
	}
	catalog.Models[0].Key = "new-chat-anchor"
	for _, row := range catalog.Models[1:] {
		if row.APIKey != "" || row.BaseURL != "" || row.ProxyID != "" || row.Generation.Connection != ref {
			t.Fatal("binding copied configuration instead of referencing its account")
		}
		target := catalog.GenerationTarget(row)
		if target.Error != "" || target.Connection == nil || target.Connection.APIKey != source.APIKey || target.Connection.ProxyID != source.ProxyID || target.Connection.BaseURL != source.BaseURL || target.Connection.Authentication != source.Authentication {
			t.Fatal("chat anchor change disconnected the saved account or changed its configuration")
		}
	}
	if _, err := catalog.EditGeneration(GenerationEdit{Kind: model.ImageOutput, Adapter: "google", Model: "image-test", Connection: ref}); err == nil {
		t.Fatal("accepted incompatible format")
	}
	catalog.Models[0].Disabled = true
	for _, row := range catalog.Models[1:] {
		if target := catalog.GenerationTarget(row); target.Error == "" || target.Connection != nil {
			t.Fatal("disabled binding silently reused another route")
		}
	}
}

func TestMediaServiceConnectionAndLegacyBinding(t *testing.T) {
	ctx := t.Context()
	raw, err := ChangeConnection(ctx, Catalog{}, false, http.MethodPost, ConnectionInput{Provider: "generation", ProviderID: "fal", APIKey: "fixture-key"})
	if err != nil {
		t.Fatal(err)
	}
	conn := raw.Models[0]
	if raw.Default != "" || len(raw.Public()) != 0 || !Editable(conn) {
		t.Fatal("media-only service was not correctly registered")
	}
	if _, err := raw.EditGeneration(GenerationEdit{Kind: model.ImageOutput, Model: "fixture-image", Connection: conn.ConnectionID}); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.EditGeneration(GenerationEdit{Kind: model.ImageOutput, Model: "fixture-image", Adapter: "fal"}); err == nil {
		t.Fatal("allowed a binding without a saved connection")
	}

	legacy := Catalog{Models: []Configured{
		{Key: "generation-image", Provider: "generation", ProviderID: "xai", ID: "test-image", BaseURL: "https://example.test/v1", APIKey: "legacy-fixture", Generation: &model.GenerationBinding{Kind: model.ImageOutput}},
		{Key: "generation-video", Provider: "generation", ProviderID: "xai", ID: "test-video", BaseURL: "https://example.test/v1", APIKey: "legacy-fixture", Generation: &model.GenerationBinding{Kind: model.VideoOutput}},
	}}
	dir := t.TempDir()
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err = configstore.Write(dir, configstore.Models, data); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil || string(before) != string(after) {
		t.Fatal("startup rewrote the settings")
	}
	if len(loaded.Models) != 3 || !loaded.Models[2].generationOnly() || len(loaded.Public()) != 0 {
		t.Fatal("legacy media credentials were not consolidated into one saved connection")
	}
	for _, binding := range loaded.Models[:2] {
		target := loaded.GenerationTarget(binding)
		if target.Error != "" || target.Connection == nil || target.Connection.APIKey != "legacy-fixture" || target.Connection.BaseURL != "https://example.test/v1" {
			t.Fatal("legacy binding lost its configured destination or credentials")
		}
	}
	edited, err := ChangeConnection(ctx, loaded, true, http.MethodPut, ConnectionInput{Provider: "generation", ProviderID: "xai", Key: loaded.Models[2].Key, BaseURL: "https://example.test/v1", Name: "Renamed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Save(dir, edited); err != nil {
		t.Fatal(err)
	}
	again, err := Load(dir)
	if err != nil || len(again.Models) != 3 || again.Models[2].APIKey != "legacy-fixture" || again.Models[2].Name != "Renamed" {
		t.Fatal("saving the shared account broke its legacy bindings")
	}
}
