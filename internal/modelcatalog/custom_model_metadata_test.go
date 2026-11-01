package modelcatalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
)

func TestCustomInventoryMetadataPrecedenceAndIsolation(t *testing.T) {
	useExplicitReasoningCatalog(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after_id") == "" {
			fmt.Fprint(w, `{"data":[
			{"id":"gpt-6-luna","reasoning":false,"supports_tools":false,"input_modalities":["text"],"context_length":8192,"max_output_tokens":1024},
			{"id":"private-rich","name":"Private Vision","architecture":{"input_modalities":["text","image"],"output_modalities":["text"]},"limits":{"max_input_tokens":16000,"max_output_tokens":4000},"supported_parameters":["tools"],"metadata":{"supported_reasoning_levels":["HIGH","low","high","invented"]}},
			{"id":"name-only","name":"A nice name"}],"has_more":true,"last_id":"name-only"}`)
		} else {
			fmt.Fprint(w, `{"data":[
			{"id":"bare"},
			{"id":"malformed","reasoning":"false","max_model_len":-1,"context_length":1.5,"supports_tools":"yes","input_modalities":["invented"],"supported_reasoning_levels":["imaginary"]},
			{"id":"fallback","max_model_len":"bad","context_length":32000,"max_output_tokens":null,"max_completion_tokens":6000,"input_modalities":7,"architecture":{"input_modalities":["text","image"]}},
			{"id":"explicit-empty","supported_reasoning_levels":[]},
			{"id":"image-generator","input_modalities":["text"],"output_modalities":["image"]},
			{"id":"vision-embedding","input_modalities":["image"],"output_modalities":["text"]},
			{"id":"overflow","context_length":9223372036854775808,"max_tokens":-1},
			{"id":"gemini-alias","displayName":"Friendly Gemini","inputTokenLimit":1000,"outputTokenLimit":2000}]}`)
		}
	}))
	defer upstream.Close()
	models, err := DiscoverCustomModels(context.Background(), Configured{Provider: "openai-chat", ProviderID: "custom", Authentication: "none", BaseURL: upstream.URL})
	if err != nil || len(models) != 9 {
		t.Fatalf("discovery: count=%d error=%v", len(models), err)
	}
	byID := map[string]Configured{}
	for _, m := range models {
		byID[m.ID] = m
	}
	known := byID["gpt-6-luna"]
	if info := known.Info(); !info.Recognized || info.CatalogReasoning || len(info.ReasoningOptions) != 0 || info.Tools == nil || *info.Tools || !slices.Equal(info.InputModalities, []string{"text"}) || info.Context != 8192 || info.OutputLimit != 1024 {
		t.Fatalf("explicit restrictions lost to catalog: %+v", info)
	}
	rich := byID["private-rich"]
	if info := rich.Info(); !info.Recognized || info.ModelName != "Private Vision" || info.Context != 20000 || info.InputLimit != 16000 || info.OutputLimit != 4000 || info.Tools == nil || !*info.Tools || !slices.Equal(info.InputModalities, []string{"text", "image"}) || !slices.Equal(info.ReasoningOptions, []string{"low", "high"}) {
		t.Fatalf("rich metadata missing: %+v", info)
	}
	for _, id := range []string{"bare", "name-only", "malformed", "overflow"} {
		info := byID[id].Info()
		if info.Recognized || info.Context != 0 || info.Tools != nil || len(info.ReasoningOptions) != 0 || len(info.InputModalities) != 0 {
			t.Errorf("unknown %q inherited or invented capabilities: %+v", id, info)
		}
	}
	if info := byID["fallback"].Info(); !info.Recognized || info.Context != 32000 || info.OutputLimit != 6000 || !slices.Contains(info.InputModalities, "image") {
		t.Fatalf("malformed extension blocked valid siblings: %+v", info)
	}
	if info := byID["gemini-alias"].Info(); info.ModelName != "Friendly Gemini" || info.Context != 3000 || info.InputLimit != 1000 || info.OutputLimit != 2000 {
		t.Fatalf("native token limits lost: %+v", info)
	}
	// No live controls means fall back to the reference, whereas an explicit
	// false or empty list must suppress that reference, including after IPC.
	for _, raw := range []string{`{}`, `{"reasoning":true}`, `{"reasoning":false}`, `{"thinking":false}`, `{"supported_reasoning_levels":[]}`} {
		m := Configured{Key: "known", Provider: "openai-responses", ProviderID: "custom", ID: "gpt-6-luna"}
		m.applyDiscoveredMetadata(discoveryObject(json.RawMessage(raw)))
		model, _, err := (Catalog{Models: []Configured{m}}).Resolve("known")
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(model)
		var transported modelcfg.Model
		if err := json.Unmarshal(data, &transported); err != nil {
			t.Fatal(err)
		}
		wantOptions := raw == `{}` || raw == `{"reasoning":true}`
		if got := len(provider.SessionReasoningOptions(transported)) > 0; got != wantOptions {
			t.Fatalf("reasoning fallback for %s: %+v", raw, transported.DiscoveredReasoning)
		}
	}
}

func TestDiscoveredCapabilitiesReachCustomRequests(t *testing.T) {
	for _, protocol := range []string{"openai-chat", "openai-responses", "anthropic", "gemini", "cohere"} {
		t.Run(protocol, func(t *testing.T) {
			var captured map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Access") != "saved-header" || r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "saved-key" {
					t.Error("discovered metadata altered custom authentication")
				}
				if r.Method == http.MethodGet {
					fmt.Fprint(w, `{"data":[{"id":"private-vision","name":"Vision alias","input_modalities":["text","image"],"context_length":16384,"limits":{"max_input_tokens":12288,"max_output_tokens":4096},"supported_reasoning_levels":["low","high"],"api":"ignore-me","base_url":"https://wrong.example","headers":{"Authorization":"wrong"}}]}`)
					return
				}
				if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				switch protocol {
				case "openai-chat":
					if r.URL.Path != "/prefix/chat/completions" {
						t.Errorf("wrong route: %s", r.URL.Path)
					}
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				case "openai-responses":
					if r.URL.Path != "/prefix/responses" {
						t.Errorf("wrong route: %s", r.URL.Path)
					}
					fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n")
				case "anthropic":
					if r.URL.Path != "/prefix/messages" {
						t.Errorf("wrong route: %s", r.URL.Path)
					}
					fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				case "gemini":
					if !strings.Contains(r.URL.Path, "/prefix/models/private-vision:streamGenerateContent") {
						t.Errorf("wrong route: %s", r.URL.Path)
					}
					fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}\n\n")
				case "cohere":
					if r.URL.Path != "/prefix/chat" {
						t.Errorf("wrong route: %s", r.URL.Path)
					}
					fmt.Fprint(w, "event: content-delta\ndata: {\"type\":\"content-delta\",\"delta\":{\"message\":{\"content\":{\"text\":\"ok\"}}}}\n\nevent: message-end\ndata: {\"type\":\"message-end\",\"delta\":{\"finish_reason\":\"COMPLETE\"}}\n\n")
				}
			}))
			defer upstream.Close()
			source := Configured{Provider: protocol, ProviderID: "custom", Authentication: "x-api-key", APIKey: "saved-key", BaseURL: upstream.URL + "/prefix", Headers: map[string]string{"X-Access": "saved-header"}}
			models, err := DiscoverCustomModels(context.Background(), source)
			if err != nil || len(models) != 1 {
				t.Fatalf("discovery: %v", err)
			}
			models[0].Key = "vision"
			catalog := Catalog{Default: "vision", Models: models}
			if err := ResolveCatalog(&catalog); err != nil {
				t.Fatal(err)
			}
			model, _, err := catalog.Resolve("")
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(model)
			if err := json.Unmarshal(data, &model); err != nil {
				t.Fatal(err)
			}
			if model.ImageInput == nil || !*model.ImageInput || model.Context != 16384 || model.InputLimit != 12288 || model.OutputLimit != 4096 {
				t.Fatalf("request capabilities lost: %+v", model)
			}
			if protocol == "cohere" {
				if len(provider.SessionReasoningOptions(model)) != 0 {
					t.Fatal("unsupported reasoning transport exposed controls")
				}
			} else if err := provider.ApplyReasoningEffort(&model, "high"); err != nil {
				t.Fatal(err)
			}
			image := provider.Image{MediaType: "image/png", Data: []byte("\x89PNG\r\n\x1a\nfixture")}
			if _, err := provider.ModelTurn(context.Background(), model, []provider.ChatMessage{{Role: "user", Content: "Inspect", Images: []provider.Image{image}}}, nil); err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(captured)
			if !strings.Contains(string(body), base64.StdEncoding.EncodeToString(image.Data)) {
				t.Fatalf("no image in request: %s", body)
			}
			switch protocol {
			case "openai-chat":
				if captured["reasoning_effort"] != "high" {
					t.Fatalf("no live effort: %s", body)
				}
			case "openai-responses":
				if !strings.Contains(string(body), `"reasoning":{"effort":"high","summary":"auto"}`) {
					t.Fatalf("no live effort/summary: %s", body)
				}
			case "anthropic":
				if captured["max_tokens"] != float64(4096) || !strings.Contains(string(body), `"output_config":{"effort":"high"}`) {
					t.Fatalf("no live limits/effort: %s", body)
				}
			case "gemini":
				if !strings.Contains(string(body), `"thinkingLevel":"HIGH"`) {
					t.Fatalf("no live effort: %s", body)
				}
			}
		})
	}
}
