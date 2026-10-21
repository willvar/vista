package provider

import (
	"net/http/httptest"
	"reflect"
	"testing"

	modelcfg "vista/internal/model"
)

func TestRequestPurposeTranslatesCopilotHeaders(t *testing.T) {
	base := modelcfg.Model{Provider: "openai-responses", ProviderID: "github-copilot", ID: "gpt-6-luna", SessionID: "session", ReasoningEffort: "high"}
	for _, item := range []struct {
		purpose     RequestPurpose
		interaction string
		session     string
		initiator   string
	}{
		{PurposeConversation, "conversation-agent", "session", "user"},
		{PurposeSubagent, "conversation-subagent", "session", "agent"},
		{PurposeCompaction, "conversation-compaction", "session", "agent"},
		{PurposeTitle, "conversation-agent", "", "user"},
	} {
		t.Run(string(item.purpose), func(t *testing.T) {
			model := ForPurpose(base, item.purpose)
			request := httptest.NewRequest("POST", "/", nil)
			copilotHeaders(request, model, []ChatMessage{{Role: "user", Content: "Hi"}})
			if request.Header.Get("X-Interaction-Type") != item.interaction || request.Header.Get("X-Interaction-Id") != item.session || request.Header.Get("x-initiator") != item.initiator {
				t.Fatalf("purpose %s headers: %v", item.purpose, request.Header)
			}
			if item.purpose != PurposeTitle && model.ReasoningEffort != "high" {
				t.Fatal("conversation or compaction changed the user's reasoning choice")
			}
		})
	}
	if base.InteractionType != "" || base.SessionID != "session" || base.ReasoningEffort != "high" {
		t.Fatal("request preparation changed the original model")
	}
}

func TestTitlePurposeOmitsReasoningAndKeepsSavedOptions(t *testing.T) {
	const protocol = "fixture-title-protocol"
	protocolAdapters[protocol] = protocolAdapter{applyReasoning: func(model *modelcfg.Model, effort string) {
		// Title preparation must not invoke protocol reasoning defaults.
		model.ThinkingLevel = "fixture-" + effort
	}}
	t.Cleanup(func() { delete(protocolAdapters, protocol) })
	enabled := true
	budget := 4096
	base := modelcfg.Model{
		Provider: protocol, ProviderID: "openai", ID: "gpt-6-luna", APIKey: "secret", SessionID: "session", InteractionType: "conversation-subagent",
		ReasoningSummary: "detailed", ReasoningEffort: "high", ChatReasoningOverride: true, Effort: "high",
		Thinking: &modelcfg.ThinkingOption{Type: "enabled"}, EnableThinking: &enabled, ThinkingBudget: &budget, ThinkingLevel: "high",
		OpenRouter: &modelcfg.OpenRouterOptions{
			AppName: "vista", Provider: &modelcfg.OpenRouterRouting{Order: []string{"chosen"}},
			Plugins:   []modelcfg.OpenRouterPlugin{{ID: "file-parser"}},
			Reasoning: &modelcfg.OpenRouterReasoning{Effort: "high"},
		},
	}
	model := ForPurpose(base, PurposeTitle)
	if !model.ReasoningDisabled || model.ThinkingLevel != "" || model.ReasoningSummary != "" || model.ReasoningEffort != "" || model.ChatReasoningOverride || model.Effort != "" || model.Thinking != nil || model.EnableThinking != nil || model.ThinkingBudget != nil {
		t.Fatal("title did not clear the previous reasoning configuration")
	}
	if model.SessionID != "" || model.InteractionType != "" || model.OpenRouter.Plugins != nil || model.OpenRouter.Reasoning != nil {
		t.Fatal("title inherited conversation identity or provider options")
	}
	if model.APIKey != base.APIKey || model.OpenRouter.AppName != base.OpenRouter.AppName || !reflect.DeepEqual(model.OpenRouter.Provider, base.OpenRouter.Provider) {
		t.Fatal("title changed authentication or routing")
	}
	if len(base.OpenRouter.Plugins) != 1 || base.OpenRouter.Reasoning.Effort != "high" || base.ThinkingLevel != "high" {
		t.Fatal("title preparation mutated saved nested options")
	}
}

func TestRequestPurposeUsesServiceAdapter(t *testing.T) {
	const service = "fixture-purpose-service"
	catalogAdapters[service] = catalogAdapter{prepareRequest: func(model *modelcfg.Model, purpose RequestPurpose) {
		model.InteractionType = "fixture-" + string(purpose)
	}}
	t.Cleanup(func() { delete(catalogAdapters, service) })
	for _, purpose := range []RequestPurpose{PurposeConversation, PurposeSubagent, PurposeCompaction, PurposeTitle} {
		model := ForPurpose(modelcfg.Model{ProviderID: service}, purpose)
		if model.InteractionType != "fixture-"+string(purpose) {
			t.Fatalf("purpose %s bypassed the service adapter", purpose)
		}
	}
}

func TestSupportedInputModalitiesUsesProtocolRegistration(t *testing.T) {
	const protocol = "fixture-image-protocol"
	declared := []string{"text", "image"}
	if got := SupportedInputModalities(protocol, declared); len(got) != 0 {
		t.Fatalf("unknown protocol advertises input support: %v", got)
	}
	protocolAdapters[protocol] = protocolAdapter{}
	t.Cleanup(func() { delete(protocolAdapters, protocol) })
	if got := SupportedInputModalities(protocol, declared); !reflect.DeepEqual(got, []string{"text"}) {
		t.Fatalf("text-only adapter advertises image support: %v", got)
	}
	protocolAdapters[protocol] = protocolAdapter{imageInput: true}
	if got := SupportedInputModalities(protocol, declared); !reflect.DeepEqual(got, declared) {
		t.Fatalf("registered image adapter requires another protocol whitelist: %v", got)
	}
	if got := SupportedInputModalities(protocol, []string{"text"}); !reflect.DeepEqual(got, []string{"text"}) {
		t.Fatalf("protocol support overrode the model's text-only declaration: %v", got)
	}
}
