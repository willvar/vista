package modelcatalog

import (
	"reflect"
	"slices"
	"testing"
)

func TestPublicModelInputCapabilitiesIntersectProtocolAndMetadata(t *testing.T) {
	for _, item := range []struct {
		protocol string
		declared []string
		want     []string
	}{
		{"openai-responses", []string{"text", "image"}, []string{"text", "image"}},
		{"openai-responses", []string{"text"}, []string{"text"}},
		{"bedrock-mantle-chat", []string{"text", "image"}, []string{"text"}},
		{"unknown-protocol", []string{"text", "image"}, nil},
	} {
		t.Run(item.protocol, func(t *testing.T) {
			model := Configured{Provider: item.protocol, ID: "fixture-model", InputModalities: item.declared}
			info := model.Info()
			if !reflect.DeepEqual(info.InputModalities, item.want) {
				t.Fatalf("public capabilities = %v, want %v", info.InputModalities, item.want)
			}
			if !reflect.DeepEqual(model.EffectiveInputModalities(), item.declared) {
				t.Fatal("public projection changed stored metadata")
			}
		})
	}
}

func TestCustomModelInputCapabilitiesUseExactModelID(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model Configured
		image bool
	}{
		{"custom chat", Configured{Provider: "openai-chat", Name: "First connection", ID: "gpt-4.1-mini"}, true},
		{"custom responses", Configured{Provider: "openai-responses", Name: "Second connection", ID: "gpt-4.1-mini"}, true},
		{"unknown ID", Configured{Provider: "openai-responses", ID: "custom-gpt-4.1-mini-unknown"}, false},
		{"text model", Configured{Provider: "openai-chat", ID: "deepseek-r1"}, false},
		{"explicit text restriction", Configured{Provider: "openai-chat", ID: "gpt-4.1-mini", InputModalities: []string{"text"}}, false},
		{"explicit unknown image model", Configured{Provider: "openai-chat", ID: "private-vision-model", InputModalities: []string{"text", "image"}}, true},
		{"directory restriction", Configured{Provider: "openai-chat", ID: "gpt-4.1-mini", inputModalities: []string{"text"}}, false},
		{"protocol restriction", Configured{Provider: "bedrock-mantle-chat", ID: "gpt-4.1-mini"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := tc.model.Info()
			if got := slices.Contains(info.InputModalities, "image"); got != tc.image {
				t.Fatalf("public image capability = %t, want %t; modalities=%v", got, tc.image, info.InputModalities)
			}
		})
	}
}

func TestPublicModelNameKeepsProviderAndRequestIdentity(t *testing.T) {
	model := Configured{Key: "my-connection-mini", Name: "My connection", Provider: "openai-chat", ID: "gpt-4.1-mini"}
	info := model.Info()
	if info.ModelName == "" || info.ModelName == model.Name || info.Name != model.Name || info.ID != model.ID || info.Key != model.Key {
		t.Fatalf("display name replaced model or provider identity: %+v", info)
	}
	request, key, err := (Catalog{Default: model.Key, Models: []Configured{model}}).Resolve("")
	if err != nil || request.ID != model.ID || key != model.Key {
		t.Fatalf("catalog display name changed request identity: id=%q key=%q err=%v", request.ID, key, err)
	}
	model.ID = "private-unknown-model"
	if info := model.Info(); info.ModelName != "" || info.ID != model.ID {
		t.Fatalf("unknown model received a guessed display name: %+v", info)
	}
}
