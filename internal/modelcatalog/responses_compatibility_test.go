package modelcatalog

import (
	"context"
	"net/http"
	"os"
	"testing"

	modelcfg "vista/internal/model"
)

func TestCodexCompatibilityIsExplicitAndSurvivesSettingsAndResolution(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	route := modelcfg.ResponsesRouteCodexCompatible
	input := ConnectionInput{Provider: "openai-responses", ProviderID: "custom", Name: "Compatible gateway", BaseURL: "http://127.0.0.1:1/v1", CredentialMode: "none", IDs: []string{"fixture"}, ResponsesRoute: &route}
	raw, err := ChangeConnection(context.Background(), Catalog{}, false, http.MethodPost, input)
	if err != nil {
		t.Fatal(err)
	}
	anchor := raw.Models[0]
	if anchor.ResponsesRoute != route || !Editable(anchor) {
		t.Fatal("explicit compatibility was dropped or made the connection uneditable")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	resolved, err := Save(dir, raw)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := resolved.ResolveForRun(context.Background(), anchor.Key)
	if err != nil || model.ResponsesRoute != route || !model.Custom || model.BaseURL != input.BaseURL || model.Authentication != "none" || model.CodexCredentialPath != "" {
		t.Fatalf("compatibility changed endpoint or credential ownership: %v", err)
	}
	input.Key, input.ResponsesRoute = anchor.Key, nil
	edited, err := ChangeConnection(context.Background(), raw, true, http.MethodPut, input)
	if err != nil || edited.Models[0].ResponsesRoute != route {
		t.Fatalf("ordinary settings edit dropped explicit compatibility: %v", err)
	}
	input.BaseURL = "http://127.0.0.1:2/v1"
	moved, err := ChangeConnection(context.Background(), edited, true, http.MethodPut, input)
	if err != nil || moved.Models[0].ResponsesRoute != modelcfg.ResponsesRouteUnknown {
		t.Fatalf("capability silently followed a changed endpoint: %v", err)
	}
}

func TestCodexCompatibilityRejectsOtherFormatsAndNativeRouteOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, tc := range []struct {
		protocol string
		route    modelcfg.ResponsesRoute
	}{
		{"openai-chat", modelcfg.ResponsesRouteCodexCompatible},
		{"anthropic", modelcfg.ResponsesRouteCodexCompatible},
		{"openai-responses", modelcfg.ResponsesRouteCodex},
		{"openai-responses", modelcfg.ResponsesRoute("unknown")},
	} {
		input := ConnectionInput{Provider: tc.protocol, ProviderID: "custom", BaseURL: "http://127.0.0.1:1/v1", CredentialMode: "none", IDs: []string{"fixture"}, ResponsesRoute: &tc.route}
		if _, err := ChangeConnection(context.Background(), Catalog{}, false, http.MethodPost, input); err == nil {
			t.Fatalf("accepted unsupported compatibility: %s %s", tc.protocol, tc.route)
		}
	}
}
