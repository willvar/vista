package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	modelcfg "vista/internal/model"
)

const modelsDevTestSource = `{
 "opencode":{"id":"opencode","models":{
  "deepseek-v4.1-flash-free":{"id":"deepseek-v4.1-flash-free","name":"DeepSeek V4.1 Flash Free","limit":{"context":200000,"output":32000},"modalities":{"input":["text","image"],"output":["text"]},"reasoning":true,"reasoning_options":[{"type":"effort","values":["low","high","max"]}],"tool_call":true,"provider":{"api":"https://untrusted.example/v1"}},
  "gpt-6.1-sol":{"id":"gpt-6.1-sol","name":"New Sol name","limit":{"context":1050000,"output":128000},"modalities":{"input":["text","image"],"output":["text"]},"reasoning":true,"reasoning_options":[{"type":"effort","values":["low","high"]}],"tool_call":true,"provider":{"npm":"@ai-sdk/anthropic"}},
  "big-pickle":{"id":"big-pickle","name":"Big Pickle","limit":{"context":200000,"output":32000},"modalities":{"input":["text"],"output":["text"]},"reasoning":true,"reasoning_options":[],"tool_call":true},
  "minimax-m2.5-free":{"id":"minimax-m2.5-free","name":"MiniMax-M2.5 Free","limit":{"context":204800,"output":131072},"modalities":{"input":["text"],"output":["text"]},"reasoning":true,"reasoning_options":[],"tool_call":true,"provider":{"npm":"@ai-sdk/anthropic"}},
  "picture":{"id":"picture","name":"Picture","modalities":{"input":["text"],"output":["image"]},"tool_call":false}
 }},
 "opencode-go":{"id":"opencode-go","models":{
  "deepseek-v4.1-flash-free":{"id":"deepseek-v4.1-flash-free","name":"Separate Go variant","limit":{"context":100000,"output":16000},"modalities":{"input":["text"],"output":["text"]},"reasoning":false,"tool_call":true}
 }}
}`

func modelsDevDownloadClient(t *testing.T, revision, ompSource, modelsDevSource string) *http.Client {
	t.Helper()
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	compressed := encoder.EncodeAll([]byte(modelsDevSource), nil)
	encoder.Close()
	omp := ompDownloadClient(t, revision, ompSource)
	return &http.Client{Transport: ompTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "catalog.stencil.so" && r.URL.Host != "models.dev" && r.URL.Host != "models.opencode.ai" {
			return omp.Transport.RoundTrip(r)
		}
		if r.URL.String() != modelsDevCatalogURL && r.URL.String() != openCodeModelsCatalogURL && r.URL.String() != stencilCatalogURL || r.Header.Get("Authorization") != "" {
			t.Error("incorrect models.dev endpoint or unexpected credential")
		}
		body := []byte(modelsDevSource)
		if r.URL.Host == "catalog.stencil.so" {
			body = compressed
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}
}

func TestModelCatalogMergesExactMetadataAndPreservesOMPPolicies(t *testing.T) {
	original := CurrentOMPCatalog()
	t.Cleanup(original.Activate)
	next, err := FetchModelCatalog(context.Background(), modelsDevDownloadClient(t, original.Info().Revision, "unused", modelsDevTestSource))
	if err != nil {
		t.Fatal(err)
	}
	if CurrentOMPCatalog() != original || next.Info().ModelsDevRevision == "" {
		t.Fatal("staged merge changed the current catalog or omitted the models.dev revision")
	}
	next.Activate()
	free, ok := OMPModelMetadata("opencode", "deepseek-v4.1-flash-free")
	if !ok || free.Name != "DeepSeek V4.1 Flash Free" || free.ContextWindow != 200000 || free.MaxTokens != 32000 || !slices.Equal(free.Input, []string{"text", "image"}) {
		t.Fatalf("exact Free metadata missing: %+v", free)
	}
	if got := ModelReasoningOptionsFor("opencode", "deepseek-v4.1-flash-free"); !slices.Equal(got, []string{"low", "high", "max"}) {
		t.Fatalf("Free reasoning options: %v", got)
	}
	route, err := ResolveCatalogModel("opencode", "deepseek-v4.1-flash-free")
	if err != nil || route.Protocol != "openai-chat" || route.BaseURL != "https://opencode.ai/zen/v1" {
		t.Fatalf("new model did not use Vista's route: %+v, %v", route, err)
	}
	request := modelcfg.Model{Provider: route.Protocol, ProviderID: "opencode", ID: "deepseek-v4.1-flash-free"}
	if err := ApplyReasoningEffort(&request, "max"); err != nil || request.ReasoningEffort != "max" {
		t.Fatalf("new model effort cannot be encoded: %+v, %v", request, err)
	}
	goFree, _ := OMPModelMetadata("opencode-go", "deepseek-v4.1-flash-free")
	if goFree.ContextWindow != 100000 || goFree.Reasoning || len(goFree.Input) != 1 {
		t.Fatalf("provider metadata crossed boundaries: %+v", goFree)
	}
	for _, id := range []string{"gpt-6.1-sol", "big-pickle", "minimax-m2.5-free"} {
		before, _ := original.model("opencode", id)
		after, _ := next.model("opencode", id)
		if before.Thinking == nil || after.Thinking == nil || before.API != after.API || before.Thinking.Mode != after.Thinking.Mode || before.Thinking.RequiresEffort != after.Thinking.RequiresEffort || !slices.Equal(before.Thinking.Efforts, after.Thinking.Efforts) {
			t.Fatalf("lost OMP policy for %s: before=%+v after=%+v", id, before, after)
		}
	}
	if got := ModelDisplayNameFor("opencode", "gpt-6.1-sol"); got != "New Sol name" {
		t.Fatalf("models.dev metadata was not primary: %q", got)
	}
	if !slices.Equal(next.modelIDs("openai-codex"), original.modelIDs("openai-codex")) || len(next.modelIDs("openai-codex")) == 0 {
		t.Fatal("models.dev removed the OMP-only Codex catalog")
	}
	if slices.Contains(next.TextModelIDs("opencode"), "picture") {
		t.Fatal("image-only model entered the text picker")
	}
	free.Thinking.Efforts[0] = "mutated"
	if next, _ := OMPModelMetadata("opencode", "deepseek-v4.1-flash-free"); next.Thinking.Efforts[0] != "low" {
		t.Fatal("merged metadata shared a mutable thinking slice")
	}
}

func TestModelsDevOnlyUpdateSurvivesReloadAndRemovesOldSupplementRows(t *testing.T) {
	original := CurrentOMPCatalog()
	t.Cleanup(original.Activate)
	client := modelsDevDownloadClient(t, original.Info().Revision, "unused", modelsDevTestSource)
	next, err := FetchModelCatalog(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := next.Save(dir); err != nil {
		t.Fatal(err)
	}
	if err := LoadOMPCatalog(dir); err != nil {
		t.Fatal(err)
	}
	if CurrentOMPCatalog().Info() != next.Info() {
		t.Fatal("saved sources did not restore the same merged catalog")
	}
	data, _ := os.ReadFile(OMPCatalogPath(dir))
	if strings.Contains(string(data), "untrusted.example") {
		t.Fatal("online endpoint was persisted as model metadata")
	}
	// Keep OMP's SHA unchanged while removing a models.dev-only ID. Never use
	// the previous merged rows as the base for the next supplement.
	replacement := strings.ReplaceAll(modelsDevTestSource, "deepseek-v4.1-flash-free", "new-free-model")
	updated, err := FetchModelCatalog(context.Background(), modelsDevDownloadClient(t, next.Info().Revision, "unused", replacement))
	if err != nil || updated.Info().ModelsDevRevision == next.Info().ModelsDevRevision {
		t.Fatalf("models.dev-only update not detected: %v", err)
	}
	if _, ok := updated.model("opencode", "deepseek-v4.1-flash-free"); ok {
		t.Fatal("removed models.dev row leaked from previous merge")
	}
	if err := updated.Save(dir); err != nil {
		t.Fatal(err)
	}
	if err := LoadOMPCatalog(dir); err != nil || CurrentOMPCatalog().Info() != updated.Info() {
		t.Fatalf("same-OMP-revision disk reload was skipped: %v", err)
	}
	original.Activate()
	if err := LoadOMPCatalog(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := OMPModelMetadata("opencode", "new-free-model"); !ok {
		t.Fatal("new process failed to restore online metadata")
	}
}

func TestModelsDevCatalogRejectsInvalidDataWithoutActivation(t *testing.T) {
	original := CurrentOMPCatalog()
	for name, data := range map[string]string{
		"empty":            `{}`,
		"truncated":        `{"opencode":`,
		"identity":         strings.Replace(modelsDevTestSource, `"id":"deepseek-v4.1-flash-free"`, `"id":"another-model"`, 1),
		"providerIdentity": strings.Replace(modelsDevTestSource, `"id":"opencode"`, `"id":"another-provider"`, 1),
		"limits":           strings.Replace(modelsDevTestSource, `"context":200000`, `"context":-1`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := FetchModelCatalog(context.Background(), modelsDevDownloadClient(t, original.Info().Revision, "unused", data))
			if err == nil || CurrentOMPCatalog() != original {
				t.Fatalf("invalid models.dev catalog changed active metadata: %v", err)
			}
		})
	}
}

func TestModelsDevRevisionIgnoresMirrorFormattingAndScores(t *testing.T) {
	base, err := decodeModelsDevCatalog([]byte(modelsDevTestSource))
	if err != nil {
		t.Fatal(err)
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(modelsDevTestSource), &content); err != nil {
		t.Fatal(err)
	}
	content["opencode"].(map[string]any)["tps"] = 123
	formatted, err := json.MarshalIndent(content, "", "    ")
	if err != nil {
		t.Fatal(err)
	}
	mirror, err := decodeModelsDevCatalog(formatted)
	if err != nil || mirror.Revision != base.Revision {
		t.Fatalf("equivalent mirror changed the revision: %v", err)
	}
	changed, err := decodeModelsDevCatalog([]byte(strings.Replace(modelsDevTestSource, `"context":200000`, `"context":100000`, 1)))
	if err != nil || changed.Revision == base.Revision {
		t.Fatalf("changed metadata retained the same revision: %v", err)
	}
}
