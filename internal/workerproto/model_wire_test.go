package workerproto

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCommandModelWireRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		check   func(*testing.T, Command)
	}{
		{
			name: "signed thinking and explicit zero values",
			fixture: `{
				"runId":"run-fixture","prompt":"Hello",
				"model":{
					"provider":"anthropic","providerId":"connection-fixture",
					"responsesRoute":"codex",
					"key":"connection-fixture/model-fixture","id":"model-fixture","wireId":"wire-fixture",
					"baseUrl":"https://api.example.test","apiKey":"fake-fixture-token","anthropicAuthToken":true,
					"tools":false,"enableThinking":false,"thinkingBudget":0,
					"thinking":{"type":"enabled","clear_thinking":false,"budget_tokens":0,
						"block_binding":{"prefix_mismatch_behavior":"error"}}
				}
			}`,
			check: func(t *testing.T, command Command) {
				m := command.Model
				if m.Provider != "anthropic" || m.ProviderID != "connection-fixture" || m.ResponsesRoute != "codex" || m.Key != "connection-fixture/model-fixture" || m.ID != "model-fixture" || m.WireID != "wire-fixture" {
					t.Fatal("model identity changed during decoding")
				}
				if m.APIKey != "fake-fixture-token" || !m.AnthropicAuthToken {
					t.Fatal("authentication changed during decoding")
				}
				if m.Tools == nil || *m.Tools || m.EnableThinking == nil || *m.EnableThinking || m.ThinkingBudget == nil || *m.ThinkingBudget != 0 {
					t.Fatal("explicit false or zero became an omitted option")
				}
				if m.Thinking == nil || m.Thinking.BlockBinding == nil || m.Thinking.BlockBinding.PrefixMismatchBehavior != "error" || m.Thinking.ClearThinking == nil || *m.Thinking.ClearThinking || m.Thinking.BudgetTokens == nil || *m.Thinking.BudgetTokens != 0 {
					t.Fatal("nested thinking configuration changed")
				}
				if m.OpenRouter != nil || m.ParallelToolCalls != nil || m.MaxCompletionTokens != nil || m.Thinking.BudgetAlias != nil {
					t.Fatal("omitted model options acquired values")
				}
			},
		},
		{
			name: "routing reasoning and plugins",
			fixture: `{
				"runId":"run-fixture","prompt":"Hello",
				"model":{
					"provider":"openai-chat","providerId":"openrouter","id":"model-fixture",
					"openRouter":{
						"api_keys":{"example":"fake-fixture-key"},"usage":false,
						"provider":{"order":["example"],"allow_fallbacks":false,
							"max_price":{"prompt":0,"completion":"0.5"}},
						"reasoning":{"enabled":false,"max_tokens":0},
						"plugins":[{"id":"file-parser","max_files":0,"pdf":{"engine":"native"}}]
					}
				}
			}`,
			check: func(t *testing.T, command Command) {
				o := command.Model.OpenRouter
				if o == nil || o.Provider == nil || o.Reasoning == nil {
					t.Fatal("nested routing or reasoning configuration disappeared")
				}
				if o.Usage == nil || *o.Usage || o.Provider.AllowFallbacks == nil || *o.Provider.AllowFallbacks || o.Reasoning.Enabled == nil || *o.Reasoning.Enabled || o.Reasoning.MaxTokens == nil || *o.Reasoning.MaxTokens != 0 {
					t.Fatal("nested explicit false or zero became an omitted option")
				}
				if len(o.Plugins) != 1 || o.Plugins[0].PDF == nil || o.Plugins[0].PDF.Engine != "native" || o.Plugins[0].MaxFiles == nil || *o.Plugins[0].MaxFiles != 0 {
					t.Fatal("plugin configuration changed")
				}
				if command.Model.Thinking != nil || o.Reasoning.Exclude != nil || o.Provider.RequireParameters != nil {
					t.Fatal("omitted nested options acquired values")
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var command Command
			if err := json.Unmarshal([]byte(test.fixture), &command); err != nil {
				t.Fatal(err)
			}
			test.check(t, command)
			encoded, err := json.Marshal(command)
			if err != nil {
				t.Fatal(err)
			}
			var before, after any
			if err := json.Unmarshal([]byte(test.fixture), &before); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &after); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("IPC model JSON changed after round trip:\nwant: %s\ngot:  %s", test.fixture, encoded)
			}
		})
	}
}
