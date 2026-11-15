package modelcatalog

import (
	"reflect"
	"slices"
	"testing"

	modelcfg "vista/internal/model"
)

func TestInventoryAssemblyKeepsConnectionAndModelRestrictions(t *testing.T) {
	no, yes := false, true
	remote := Configured{ID: "model", Provider: "anthropic", ProviderID: "custom", BaseURL: "https://old.invalid", APIKey: "old-secret", ProxyID: "old-proxy", Tools: &yes, Context: 32000, inputLimit: 24000, outputLimit: 8000, inputModalities: []string{"text", "image"}, outputModalities: []string{"text"}, discoveredName: "Advertised name", metadataKnown: true}
	current := Configured{Key: "saved", ConnectionID: "connection", ID: "model", Provider: "openai-responses", ProviderID: "custom", BaseURL: "https://current.invalid", APIKey: "current-secret", ProxyID: "current-proxy", Authentication: "x-api-key", Headers: map[string]string{"X-Account": "current"}, ResponsesRoute: modelcfg.ResponsesRouteCodexCompatible, PromptCaching: &modelcfg.PromptCaching{Format: "none"}, Tools: &no, Context: 16000, InputModalities: []string{"text"}}
	assembled := current.WithInventoryModel(remote.InventoryMetadata())
	if assembled.Provider != current.Provider || assembled.BaseURL != current.BaseURL || assembled.APIKey != current.APIKey || assembled.ProxyID != current.ProxyID || assembled.Authentication != current.Authentication || !reflect.DeepEqual(assembled.Headers, current.Headers) || assembled.ResponsesRoute != current.ResponsesRoute || !reflect.DeepEqual(assembled.PromptCaching, current.PromptCaching) || assembled.Key != "saved" || assembled.ConnectionID != "connection" {
		t.Fatalf("cached metadata changed current connection: %+v", assembled)
	}
	if assembled.Context != 16000 || assembled.Tools == nil || *assembled.Tools || !slices.Equal(assembled.EffectiveInputModalities(), []string{"text"}) || assembled.Info().InputLimit != 24000 || assembled.Info().ModelName != "Advertised name" {
		t.Fatalf("model restrictions or advertised metadata were lost: %+v", assembled.Info())
	}
	other := current.DiscoveryTemplate().WithInventoryModel(remote.InventoryMetadata())
	if other.Key == assembled.Key || other.Context != 32000 || !*other.Tools || !slices.Contains(other.EffectiveInputModalities(), "image") {
		t.Fatalf("anchor restrictions leaked to a discovered model: %+v", other.Info())
	}
}
