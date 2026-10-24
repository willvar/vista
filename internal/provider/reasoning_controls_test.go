package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
	"vista/internal/testcatalog"
)

func useExplicitReasoningCatalog(t *testing.T) {
	t.Helper()
	original := CurrentOMPCatalog()
	t.Cleanup(original.Activate)
	testcatalog.Install(t, LoadOMPCatalog)
}

func TestExplicitReasoningControlsReachWireWithoutOMPFallback(t *testing.T) {
	useExplicitReasoningCatalog(t)
	beam := modelcfg.Model{Provider: "openai-chat", ProviderID: "opencode-go", ID: "beam-free"}
	if got := SessionReasoningOptions(beam); !slices.Equal(got, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("Beam controls: %v", got)
	}
	if err := ApplyReasoningEffort(&beam, "minimal"); err == nil {
		t.Fatal("OMP's inferred minimal effort was accepted")
	}
	cases := []struct {
		provider, protocol, id, choice string
		want                           string
		absent                         []string
	}{
		{"opencode-go", "openai-chat", "beam-free", "max", `{"reasoning_effort":"max"}`, []string{"thinking_budget", "enable_thinking"}},
		{"anthropic", "anthropic", "claude-haiku-4-5", "budget:8192", `{"thinking":{"type":"enabled","budget_tokens":8192}}`, []string{"reasoning_effort", "output_config"}},
		{"google", "gemini", "gemini-2.5-flash", "budget:24576", `{"generationConfig":{"thinkingConfig":{"thinkingBudget":24576}}}`, []string{"reasoning_effort"}},
		{"google", "gemini", "gemini-2.5-flash", "off", `{"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}`, []string{"reasoning_effort"}},
		{"google", "gemini", "gemini-2.5-flash", "on", `{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`, []string{"reasoning_effort"}},
		{"minimax", "openai-chat", "MiniMax-M3", "off", `{"thinking":{"type":"disabled"}}`, []string{"reasoning_effort", "thinking_budget"}},
		{"minimax", "openai-chat", "MiniMax-M3", "on", `{"thinking":{"type":"adaptive"}}`, []string{"reasoning_effort", "thinking_budget"}},
		{"minimax", "anthropic", "MiniMax-M3", "off", `{"thinking":{"type":"disabled"}}`, []string{"reasoning_effort", "output_config"}},
		{"minimax", "anthropic", "MiniMax-M3", "on", `{"thinking":{"type":"adaptive"}}`, []string{"reasoning_effort", "output_config"}},
		{"opencode-go", "anthropic", "qwen3.7-plus", "budget:8192", `{"thinking":{"type":"enabled","budget_tokens":8192}}`, []string{"reasoning_effort"}},
	}
	for _, tc := range cases {
		t.Run(tc.provider+"/"+tc.id+"/"+tc.choice, func(t *testing.T) {
			var captured map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
					t.Error(err)
				}
				http.Error(w, "fixture ends after request encoding", http.StatusBadRequest)
			}))
			defer upstream.Close()
			model := modelcfg.Model{Provider: tc.protocol, ProviderID: tc.provider, ID: tc.id, BaseURL: upstream.URL, APIKey: "fixture", OutputLimit: 65536}
			if err := ApplyReasoningEffort(&model, tc.choice); err != nil {
				t.Fatal(err)
			}
			if got := ModelReasoningEffort(model); got != tc.choice {
				t.Fatalf("display choice=%s, want %s", got, tc.choice)
			}
			_, _ = ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if captured == nil {
				t.Fatal("request did not reach the simulated provider")
			}
			var want map[string]any
			_ = json.Unmarshal([]byte(tc.want), &want)
			assertRequestSubset(t, captured, want)
			for _, field := range tc.absent {
				if _, exists := captured[field]; exists {
					t.Errorf("unexpected %s in %v", field, captured)
				}
			}
		})
	}
}

func TestReasoningBudgetsUsePublishedBoundsAndTransportLimits(t *testing.T) {
	useExplicitReasoningCatalog(t)
	for _, tc := range []struct {
		provider, protocol, id, invalid string
		wantMax                         string
	}{
		{"google", "gemini", "gemini-2.5-flash", "budget:32768", "budget:24576"},
		{"anthropic", "anthropic", "claude-haiku-4-5", "budget:32768", "budget:16000"},
	} {
		model := modelcfg.Model{Provider: tc.protocol, ProviderID: tc.provider, ID: tc.id, OutputLimit: 65536}
		choices := SessionReasoningOptions(model)
		if choices[len(choices)-1] != tc.wantMax {
			t.Fatalf("%s maximum=%v", tc.id, choices)
		}
		if err := ApplyReasoningEffort(&model, tc.invalid); err == nil {
			t.Fatalf("%s accepted %s", tc.id, tc.invalid)
		}
	}
	model := modelcfg.Model{Provider: "anthropic", ProviderID: "anthropic", ID: "claude-haiku-4-5", OutputLimit: 1000}
	if choices := SessionReasoningOptions(model); len(choices) != 0 {
		t.Fatalf("budget presets exceed the request's output limit: %v", choices)
	}
}

func TestModelsDevReasoningDecodePreservesControlsAndExplicitDefaults(t *testing.T) {
	source := rewriteModelsDevSource(t, modelsDevTestSource, func(rows map[string]map[string]modelsDevModel) {
		row := rows["opencode"]["deepseek-v4.1-flash-free"]
		minimum, maximum := 0, 24576
		row.ReasoningOptions = []ReasoningOption{{Type: "effort", Values: []string{"none", "", "low", "max"}}, {Type: "toggle"}, {Type: "budget_tokens", Min: &minimum, Max: &maximum}}
		rows["opencode"][row.ID] = row
	})
	// A null entry is the source's explicit provider-default option.
	source = strings.Replace(source, `"values":["none","","low","max"]`, `"values":["none",null,"low","max"]`, 1)
	catalog := testModelsDevCatalog(t, source)
	row := catalog.Providers["opencode-zen"]["deepseek-v4.1-flash-free"]
	if len(row.ReasoningOptions) != 3 || !slices.Equal(row.ReasoningOptions[0].Values, []string{"none", "default", "low", "max"}) || *row.ReasoningOptions[2].Min != 0 || *row.ReasoningOptions[2].Max != 24576 {
		t.Fatalf("lost published controls: %+v", row.ReasoningOptions)
	}
	for _, options := range [][]ReasoningOption{
		{{Type: "budget_tokens", Min: new(100), Max: new(99)}},
		{{Type: "budget_tokens", Min: new(-2)}},
		{{Type: "toggle"}, {Type: "toggle"}},
	} {
		if err := validateReasoningOptions(options); err == nil {
			t.Fatalf("invalid controls accepted: %+v", options)
		}
	}
}

func TestLegacyModelsDevCacheRefreshesControlsDespiteSavedETag(t *testing.T) {
	current := testModelsDevCatalog(t, modelsDevTestSource)
	current.Schema = 0
	current.ETags = map[string]string{modelsDevCatalogURL: `"old-shape"`}
	client := &http.Client{Transport: ompTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("If-None-Match") != "" {
			t.Error("legacy cache reused an ETag for incompletely parsed metadata")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(modelsDevTestSource)), Header: http.Header{}}, nil
	})}
	next, _, err := downloadModelsDevCatalog(context.Background(), client, modelsDevCatalogURL, current)
	if err != nil || next.Schema != modelsDevSchema {
		t.Fatalf("legacy cache was not refreshed: %v", err)
	}
}

func TestReasoningControlsSurviveSaveAndReload(t *testing.T) {
	useExplicitReasoningCatalog(t)
	before, _ := OMPModelMetadata("google", "gemini-2.5-flash")
	dir := t.TempDir()
	if err := CurrentOMPCatalog().Save(dir); err != nil {
		t.Fatal(err)
	}
	if err := RestoreBundledOMPCatalog(); err != nil {
		t.Fatal(err)
	}
	if err := LoadOMPCatalog(dir); err != nil {
		t.Fatal(err)
	}
	after, _ := OMPModelMetadata("google", "gemini-2.5-flash")
	if !reflect.DeepEqual(before.ReasoningOptions, after.ReasoningOptions) || *after.ReasoningOptions[1].Min != 0 || *after.ReasoningOptions[1].Max != 24576 {
		t.Fatalf("lost persisted controls: %+v", after.ReasoningOptions)
	}
	*after.ReasoningOptions[1].Max = 1
	fresh, _ := OMPModelMetadata("google", "gemini-2.5-flash")
	if *fresh.ReasoningOptions[1].Max != 24576 {
		t.Fatal("returned reasoning bounds mutated the installed snapshot")
	}
}

func TestDefaultReasoningClearsPreviousRequestControls(t *testing.T) {
	original := CurrentOMPCatalog()
	t.Cleanup(original.Activate)
	file := original.data
	file.ModelsDev = testModelsDevCatalog(t, strings.Replace(modelsDevTestSource, `"low","high","max"`, `"max","low",null,"none"`, 1))
	data, _ := json.Marshal(file)
	catalog, err := decodeOMPCatalog(data)
	if err != nil {
		t.Fatal(err)
	}
	catalog.Activate()
	model := modelcfg.Model{Provider: "openai-chat", ProviderID: "opencode", ID: "deepseek-v4.1-flash-free"}
	if got := SessionReasoningOptions(model); !slices.Equal(got, []string{"default", "none", "low", "max"}) {
		t.Fatalf("default disrupted highest reasoning choice: %v", got)
	}
	if err := ApplyReasoningEffort(&model, "none"); err != nil || model.ReasoningEffort != "none" {
		t.Fatalf("explicit none did not reach the wire setting: %v", err)
	}
	model.Effort, model.ThinkingLevel = "high", "high"
	model.Thinking = &modelcfg.ThinkingOption{Type: "adaptive"}
	model.EnableThinking, model.ThinkingBudget = new(true), new(8192)
	model.OpenRouter = &modelcfg.OpenRouterOptions{Reasoning: &modelcfg.OpenRouterReasoning{Effort: "high"}}
	if err := ApplyReasoningEffort(&model, "default"); err != nil {
		t.Fatal(err)
	}
	if ModelReasoningEffort(model) != "default" || ModelSelectionEffort(model) != "default" || model.ChatReasoningOverride || model.Thinking != nil || model.EnableThinking != nil || model.ThinkingBudget != nil || model.OpenRouter.Reasoning != nil || model.ReasoningEffort != "" || model.Effort != "" || model.ThinkingLevel != "" {
		t.Fatal("provider default retained previous request reasoning settings")
	}
}

func TestOMPOnlyModelUsesThinkingPolicy(t *testing.T) {
	source := `{"fixture":{"legacy":{"id":"legacy","provider":"fixture","name":"Legacy","api":"openai-completions","reasoning":true,"input":["text"],"contextWindow":10000,"maxTokens":4000,"thinking":{"mode":"effort","efforts":["minimal","low","high"]},"reasoningOptions":[{"type":"effort","values":["max"]}]}}}`
	catalog, err := decodeOMPSourceCatalog([]byte(source), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	metadata, ok := catalog.model("fixture", "legacy")
	if !ok || metadata.ContextWindow != 10000 || metadata.Thinking == nil || len(metadata.ReasoningOptions) != 0 {
		t.Fatalf("OMP source lost its policy or supplied models.dev-only controls: %+v", metadata)
	}
	original := CurrentOMPCatalog()
	t.Cleanup(original.Activate)
	catalog.Activate()
	model := modelcfg.Model{Provider: "openai-chat", ProviderID: "fixture", ID: "legacy"}
	if got := SessionReasoningOptions(model); !slices.Equal(got, []string{"minimal", "low", "high"}) {
		t.Fatalf("OMP-only controls: %v", got)
	}
	if err := ApplyReasoningEffort(&model, "high"); err != nil || model.ReasoningEffort != "high" {
		t.Fatalf("OMP-only effort could not be applied: %+v, %v", model, err)
	}
}

func TestModelsDevReplacesReasoningPolicyByModel(t *testing.T) {
	original := CurrentOMPCatalog()
	t.Cleanup(original.Activate)
	rows := map[string]OMPModel{}
	for _, id := range []string{"native", "empty", "disabled", "omp-only"} {
		rows[id] = OMPModel{Name: id, API: "openai-completions", Reasoning: true, Input: []string{"text"},
			Thinking: &OMPThinking{Mode: "effort", Efforts: []string{"minimal", "low", "high"}, EffortMap: map[string]string{"high": "max"}}}
	}
	file := ompCatalogFile{Source: "oh-my-pi", Revision: "fixture", ModelCount: len(rows), Providers: map[string]map[string]OMPModel{"fixture": rows}}
	file.ModelsDev = testModelsDevCatalog(t, `{"fixture":{"id":"fixture","models":{
		"native":{"id":"native","name":"Native","reasoning":true,"modalities":{"input":["text"],"output":["text"]},"reasoning_options":[{"type":"effort","values":["low","high"]}]},
		"empty":{"id":"empty","name":"Empty","reasoning":true,"modalities":{"input":["text"],"output":["text"]}},
		"disabled":{"id":"disabled","name":"Disabled","reasoning":false,"modalities":{"input":["text"],"output":["text"]}}
	}}}`)
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := decodeOMPCatalog(data)
	if err != nil {
		t.Fatal(err)
	}
	catalog.Activate()
	check := func(t *testing.T) {
		t.Helper()
		for _, tc := range []struct {
			id   string
			want []string
			wire string
		}{
			{"native", []string{"low", "high"}, "high"},
			{"empty", nil, ""},
			{"disabled", nil, ""},
			{"omp-only", []string{"minimal", "low", "high"}, "max"},
		} {
			model := modelcfg.Model{Provider: "openai-chat", ProviderID: "fixture", ID: tc.id}
			if got := SessionReasoningOptions(model); !slices.Equal(got, tc.want) {
				t.Errorf("%s controls=%v, want %v", tc.id, got, tc.want)
			}
			err := ApplyReasoningEffort(&model, "high")
			if tc.wire == "" {
				if err == nil {
					t.Errorf("%s filled a missing models.dev control from OMP", tc.id)
				}
			} else if err != nil || model.ReasoningEffort != tc.wire {
				t.Errorf("%s wire effort=%s, want %s, error=%v", tc.id, model.ReasoningEffort, tc.wire, err)
			}
		}
	}
	t.Run("merged", check)
	dir := t.TempDir()
	if err := catalog.Save(dir); err != nil {
		t.Fatal(err)
	}
	original.Activate()
	if err := LoadOMPCatalog(dir); err != nil {
		t.Fatal(err)
	}
	t.Run("reloaded", check)
}

func TestOMPOnlyReasoningTranslationsReachProviderPayload(t *testing.T) {
	original := CurrentOMPCatalog()
	t.Cleanup(original.Activate)
	if err := RestoreBundledOMPCatalog(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		provider, protocol, id, choice string
		want                           string
	}{
		{"openai-codex", "openai-responses", "gpt-6-luna", "max", `{"reasoning":{"effort":"max"}}`},
		{"anthropic", "anthropic", "claude-haiku-4-5", "high", `{"thinking":{"type":"enabled","budget_tokens":16000}}`},
		{"anthropic", "anthropic", "claude-opus-4-5", "xhigh", `{"thinking":{"type":"enabled","budget_tokens":16000},"output_config":{"effort":"high"}}`},
		{"anthropic", "anthropic", "claude-fable-5", "max", `{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`},
		{"google", "gemini", "gemini-2.5-flash", "high", `{"generationConfig":{"thinkingConfig":{"thinkingBudget":16384}}}`},
		{"google", "gemini", "gemini-3-flash-preview", "high", `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"HIGH"}}}`},
		{"minimax", "openai-chat", "MiniMax-M2", "high", `{"thinking":{"type":"adaptive"}}`},
		{"openrouter", "openai-chat", "aion-labs/aion-2.0", "high", `{"reasoning":{"effort":"high"}}`},
	} {
		t.Run(tc.provider+"/"+tc.id, func(t *testing.T) {
			var captured map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
					t.Error(err)
				}
				http.Error(w, "fixture ends after request encoding", http.StatusBadRequest)
			}))
			defer upstream.Close()
			model := modelcfg.Model{Provider: tc.protocol, ProviderID: tc.provider, ID: tc.id, APIKey: "fixture", BaseURL: upstream.URL, OutputLimit: 65536}
			if err := ApplyReasoningEffort(&model, tc.choice); err != nil {
				t.Fatal(err)
			}
			if ModelReasoningEffort(model) != tc.choice {
				t.Fatalf("fallback translation lost the selected choice: %+v", model)
			}
			_, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if captured == nil {
				t.Fatalf("fallback request was rejected before HTTP: %v", err)
			}
			var want map[string]any
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			assertRequestSubset(t, captured, want)
		})
	}
}

func TestModelsDevParserUpgradeDoesNotRequireMassChangeConfirmation(t *testing.T) {
	current := testModelsDevCatalog(t, modelsDevTestSource)
	current.Schema = 0
	base := current.Providers["opencode-zen"]["gpt-6.1-sol"]
	current.Providers = map[string]map[string]OMPModel{"opencode-zen": {}}
	candidate := &modelsDevCatalog{Schema: modelsDevSchema, Providers: map[string]map[string]OMPModel{"opencode-zen": {}}}
	for i := range 12 {
		id := string(rune('a' + i))
		row := base
		row.ReasoningOptions = nil
		current.Providers["opencode-zen"][id] = row
		candidate.Providers["opencode-zen"][id] = base
	}
	current.ModelCount, candidate.ModelCount = 12, 12
	if changes := modelsDevChangesToConfirm(current, candidate); len(changes) != 0 {
		t.Fatalf("parser upgrade counted new fields as upstream mass edits: %v", changes)
	}
	current.Schema = modelsDevSchema
	for id, row := range candidate.Providers["opencode-zen"] {
		current.Providers["opencode-zen"][id] = row
	}
	row := base
	row.ReasoningOptions = []ReasoningOption{{Type: "toggle"}, {Type: "budget_tokens", Min: new(128), Max: new(24576)}}
	current.Providers["opencode-zen"]["b"] = row
	row.ReasoningOptions = []ReasoningOption{{Type: "budget_tokens", Min: new(128), Max: new(8192)}}
	candidate.Providers["opencode-zen"]["b"] = row
	if changes := modelsDevChangesToConfirm(current, candidate); len(changes) != 1 || changes[0].ModelID != "b" || changes[0].Field != "reasoningOptions" {
		t.Fatalf("budget/toggle loss escaped confirmation: %v", changes)
	}
}
