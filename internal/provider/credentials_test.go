package provider

import (
	"context"
	"errors"
	"reflect"
	"testing"

	modelcfg "vista/internal/model"
)

func TestRunCredentialsUsesProviderBeforeProtocol(t *testing.T) {
	const service, protocol = "fixture-service", "fixture-protocol"
	t.Cleanup(func() {
		delete(catalogAdapters, service)
		delete(protocolAdapters, protocol)
	})
	var calls []string
	resolve := func(name string) func(context.Context, modelcfg.Model, string) (modelcfg.Model, string, error) {
		return func(_ context.Context, model modelcfg.Model, selected string) (modelcfg.Model, string, error) {
			calls = append(calls, name)
			model.APIKey = name + "-run-token"
			return model, selected, nil
		}
	}
	catalogAdapters[service] = catalogAdapter{prepareRun: resolve("service")}
	protocolAdapters[protocol] = protocolAdapter{prepareRun: resolve("protocol")}
	input := modelcfg.Model{ProviderID: service, Provider: protocol, APIKey: "saved-secret"}
	model, key, err := ResolveRunCredentials(context.Background(), input, "chosen")
	if err != nil || key != "chosen" || model.APIKey != "service-run-token" || !reflect.DeepEqual(calls, []string{"service"}) {
		t.Fatalf("service credential precedence: key=%q calls=%v err=%v", key, calls, err)
	}
	delete(catalogAdapters, service)
	model, key, err = ResolveRunCredentials(context.Background(), input, "chosen")
	if err != nil || key != "chosen" || model.APIKey != "protocol-run-token" || !reflect.DeepEqual(calls, []string{"service", "protocol"}) {
		t.Fatalf("protocol credential fallback: key=%q calls=%v err=%v", key, calls, err)
	}
	delete(protocolAdapters, protocol)
	model, key, err = ResolveRunCredentials(context.Background(), input, "chosen")
	if err != nil || key != "chosen" || !reflect.DeepEqual(model, input) {
		t.Fatal("a provider without credential exchange changed the selected model")
	}
	if input.APIKey != "saved-secret" {
		t.Fatal("credential exchange changed the stored configuration")
	}
}

func TestRunCredentialFailureDoesNotFallBack(t *testing.T) {
	const service, protocol = "fixture-failed-service", "fixture-failed-protocol"
	t.Cleanup(func() {
		delete(catalogAdapters, service)
		delete(protocolAdapters, protocol)
	})
	failure := errors.New("credential fixture failure")
	catalogAdapters[service] = catalogAdapter{prepareRun: func(context.Context, modelcfg.Model, string) (modelcfg.Model, string, error) {
		return modelcfg.Model{}, "", failure
	}}
	protocolAdapters[protocol] = protocolAdapter{prepareRun: func(context.Context, modelcfg.Model, string) (modelcfg.Model, string, error) {
		t.Fatal("credential failure fell through to another authentication method")
		return modelcfg.Model{}, "", nil
	}}
	model, key, err := ResolveRunCredentials(context.Background(), modelcfg.Model{ProviderID: service, Provider: protocol, APIKey: "saved-secret"}, "chosen")
	if !errors.Is(err, failure) || key != "" || model.APIKey != "" {
		t.Fatalf("credential failure was not propagated: key=%q err=%v", key, err)
	}
}

func TestCatalogValidationUsesExecutionProtocolRegistry(t *testing.T) {
	const protocol = "fixture-added-protocol"
	item := CatalogConfig{Key: "test", Provider: protocol, ID: "test-model", APIKey: "fixture"}
	if err := ValidateCatalogProtocol(item, CatalogMetadata{}); err == nil {
		t.Fatal("unregistered protocol was accepted")
	}
	protocolAdapters[protocol] = protocolAdapter{turn: func(context.Context, modelcfg.Model, []ChatMessage, []map[string]any, func(string) error, func(string) error) (Turn, error) {
		return Turn{Text: "fixture reply"}, nil
	}}
	t.Cleanup(func() { delete(protocolAdapters, protocol) })
	if err := ValidateCatalogProtocol(item, CatalogMetadata{}); err != nil {
		t.Fatalf("registered protocol still needs a second whitelist: %v", err)
	}
	item.APIKey = ""
	if err := ValidateCatalogProtocol(item, CatalogMetadata{}); err == nil {
		t.Fatal("a protocol without ambient credentials accepted a missing key")
	}
	adapter := protocolAdapters[protocol]
	adapter.ambientCredentials = true
	protocolAdapters[protocol] = adapter
	if err := ValidateCatalogProtocol(item, CatalogMetadata{}); err != nil {
		t.Fatalf("protocol authentication policy needs a second whitelist: %v", err)
	}
	turn, err := ModelTurn(context.Background(), modelcfg.Model{Provider: protocol}, nil, nil)
	if err != nil || turn.Text != "fixture reply" {
		t.Fatalf("validated protocol cannot execute: turn=%+v err=%v", turn, err)
	}
}
