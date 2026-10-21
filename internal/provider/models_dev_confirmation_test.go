package provider

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func rewriteModelsDevSource(t *testing.T, source string, edit func(map[string]map[string]modelsDevModel)) string {
	t.Helper()
	var providers map[string]struct {
		ID     string                    `json:"id"`
		Models map[string]modelsDevModel `json:"models"`
	}
	if err := json.Unmarshal([]byte(source), &providers); err != nil {
		t.Fatal(err)
	}
	rows := map[string]map[string]modelsDevModel{}
	for id, provider := range providers {
		rows[id] = provider.Models
	}
	edit(rows)
	data, err := json.Marshal(providers)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestModelsDevConfirmsEachChangedFieldDespiteUnrelatedMirrorDifferences(t *testing.T) {
	tests := []struct {
		field string
		edit  func(map[string]modelsDevModel)
	}{
		{"presence", func(rows map[string]modelsDevModel) { delete(rows, "deepseek-v4.1-flash-free") }},
		{"contextWindow", func(rows map[string]modelsDevModel) {
			row := rows["deepseek-v4.1-flash-free"]
			row.Limit.Context = 50000
			rows[row.ID] = row
		}},
		{"maxTokens", func(rows map[string]modelsDevModel) {
			row := rows["deepseek-v4.1-flash-free"]
			row.Limit.Output = 8000
			rows[row.ID] = row
		}},
		{"api", func(rows map[string]modelsDevModel) {
			row := rows["deepseek-v4.1-flash-free"]
			row.Provider.Package = "@ai-sdk/anthropic"
			rows[row.ID] = row
		}},
		{"supportsTools", func(rows map[string]modelsDevModel) {
			row := rows["deepseek-v4.1-flash-free"]
			row.Tools = new(false)
			rows[row.ID] = row
		}},
		{"input", func(rows map[string]modelsDevModel) {
			row := rows["deepseek-v4.1-flash-free"]
			row.Modalities.Input = []string{"text"}
			rows[row.ID] = row
		}},
		{"thinking.efforts", func(rows map[string]modelsDevModel) {
			row := rows["deepseek-v4.1-flash-free"]
			row.ReasoningOptions[0].Values = []string{"low", "high"}
			rows[row.ID] = row
		}},
	}
	for _, test := range tests {
		t.Run(test.field, func(t *testing.T) {
			current := testModelsDevCatalog(t, modelsDevTestSource)
			candidate := rewriteModelsDevSource(t, modelsDevTestSource, func(rows map[string]map[string]modelsDevModel) { test.edit(rows["opencode"]) })
			witness := rewriteModelsDevSource(t, candidate, func(providers map[string]map[string]modelsDevModel) {
				rows := providers["opencode"]
				row := rows["gpt-6.1-sol"]
				row.Name = "Unrelated newer name"
				rows[row.ID] = row
				row.ID, row.Name = "extra-chat-model", "Unrelated addition"
				rows[row.ID] = row
			})
			var calls []string
			client := testModelsDevMirrors(t, map[string]string{modelsDevCatalogURL: candidate, stencilCatalogURL: witness}, &calls)
			next, err := fetchModelsDevMirrors(context.Background(), client, []string{modelsDevCatalogURL, stencilCatalogURL}, current, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if next.Revision != testModelsDevCatalog(t, candidate).Revision || len(calls) != 2 {
				t.Fatalf("did not install the whole selected candidate: calls=%v", calls)
			}
			if _, ok := next.Providers["opencode-zen"]["extra-chat-model"]; ok {
				t.Fatal("unrelated witness rows were merged into the selected candidate")
			}
		})
	}
}

func TestModelsDevRejectsConflictingFieldsAndReportsExactModelAndValues(t *testing.T) {
	current := testModelsDevCatalog(t, modelsDevTestSource)
	before, _ := json.Marshal(current)
	candidate := rewriteModelsDevSource(t, modelsDevTestSource, func(providers map[string]map[string]modelsDevModel) {
		row := providers["opencode"]["big-pickle"]
		row.Limit.Context, row.Limit.Output = 50000, 8000
		providers["opencode"][row.ID] = row
	})
	witness := rewriteModelsDevSource(t, candidate, func(providers map[string]map[string]modelsDevModel) {
		row := providers["opencode"]["big-pickle"]
		row.Limit.Output = 16000
		providers["opencode"][row.ID] = row
	})
	var calls []string
	client := testModelsDevMirrors(t, map[string]string{modelsDevCatalogURL: candidate, stencilCatalogURL: witness}, &calls)
	next, err := fetchModelsDevMirrors(context.Background(), client, []string{modelsDevCatalogURL, stencilCatalogURL}, current, time.Now())
	if err == nil || next != nil {
		t.Fatalf("a partially confirmed update was accepted: %v", err)
	}
	for _, fragment := range []string{"update rejected", "downloaded catalog", "opencode-zen/big-pickle maxTokens", "32000 -> 8000", "has 16000"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("missing diagnostic %q: %v", fragment, err)
		}
	}
	if strings.Contains(err.Error(), "downloads failed") {
		t.Fatal("successful downloads were reported as download failures")
	}
	after, _ := json.Marshal(current)
	if string(before) != string(after) {
		t.Fatal("rejected update mutated installed data")
	}
	if len(calls) != 2 || calls[0] == calls[1] {
		t.Fatalf("a source was downloaded repeatedly: %v", calls)
	}
}

func TestModelsDevMissingTypesUseExplicitEvidenceInEitherMirrorOrder(t *testing.T) {
	polluted := rewriteModelsDevSource(t, modelsDevTestSource, func(providers map[string]map[string]modelsDevModel) {
		row := providers["opencode"]["big-pickle"]
		row.ID, row.Name, row.Type = "embedding-model", "An embedding model", ""
		providers["opencode"][row.ID] = row
		providers["opencode-go"][row.ID] = row // Same ID, different provider.
	})
	official := rewriteModelsDevSource(t, polluted, func(providers map[string]map[string]modelsDevModel) {
		delete(providers["opencode"], "big-pickle")
		row := providers["opencode"]["embedding-model"]
		row.Type = "embedding"
		providers["opencode"][row.ID] = row
	})
	mirror := rewriteModelsDevSource(t, official, func(providers map[string]map[string]modelsDevModel) {
		row := providers["opencode"]["embedding-model"]
		row.Type = ""
		providers["opencode"][row.ID] = row
		row = providers["opencode"]["gpt-6.1-sol"]
		row.Name = "Unrelated name on the mirror"
		providers["opencode"][row.ID] = row
	})
	for _, urls := range [][]string{{modelsDevCatalogURL, stencilCatalogURL}, {stencilCatalogURL, modelsDevCatalogURL}} {
		t.Run(urls[0], func(t *testing.T) {
			current := testModelsDevCatalog(t, polluted)
			before, _ := json.Marshal(current)
			var calls []string
			client := testModelsDevMirrors(t, map[string]string{modelsDevCatalogURL: official, stencilCatalogURL: mirror}, &calls)
			next, err := fetchModelsDevMirrors(context.Background(), client, urls, current, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := next.Providers["opencode-zen"]["embedding-model"]; ok || next.Types["opencode-zen"]["embedding-model"] != "embedding" {
				t.Fatal("missing mirror type reintroduced a non-conversation model or discarded its classification")
			}
			if _, ok := next.Providers["opencode-go"]["embedding-model"]; !ok {
				t.Fatal("type information crossed provider boundaries")
			}
			after, _ := json.Marshal(current)
			if string(before) != string(after) {
				t.Fatal("type completion mutated installed metadata")
			}
			// Type knowledge survives disk reload and remains useful when only
			// the mirror with a missing type can be reached on the next check.
			file := CurrentOMPCatalog().data
			file.ModelsDev = next
			data, _ := json.Marshal(file)
			snapshot, err := decodeOMPCatalog(data)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if err := snapshot.Save(dir); err != nil {
				t.Fatal(err)
			}
			data, err = os.ReadFile(OMPCatalogPath(dir))
			if err != nil {
				t.Fatal(err)
			}
			reloaded, err := decodeOMPCatalog(data)
			if err != nil {
				t.Fatal(err)
			}
			calls = nil
			next, err = fetchModelsDevMirrors(context.Background(), client, []string{stencilCatalogURL}, reloaded.data.ModelsDev, time.Now())
			if err != nil || next.Types["opencode-zen"]["embedding-model"] != "embedding" {
				t.Fatalf("saved type knowledge was lost: %v", err)
			}
			if _, ok := next.Providers["opencode-zen"]["embedding-model"]; ok {
				t.Fatal("mirror-only check reintroduced an embedding model")
			}
		})
	}
}

func TestModelsDevExplicitTypeConflictDoesNotConfirmRemoval(t *testing.T) {
	current := testModelsDevCatalog(t, modelsDevTestSource)
	classified := rewriteModelsDevSource(t, modelsDevTestSource, func(providers map[string]map[string]modelsDevModel) {
		row := providers["opencode"]["big-pickle"]
		row.Type = "reranking"
		providers["opencode"][row.ID] = row
	})
	chat := rewriteModelsDevSource(t, modelsDevTestSource, func(providers map[string]map[string]modelsDevModel) {
		row := providers["opencode"]["big-pickle"]
		row.Type = "chat"
		providers["opencode"][row.ID] = row
	})
	var calls []string
	client := testModelsDevMirrors(t, map[string]string{modelsDevCatalogURL: classified, stencilCatalogURL: chat}, &calls)
	if _, err := fetchModelsDevMirrors(context.Background(), client, []string{modelsDevCatalogURL, stencilCatalogURL}, current, time.Now()); err == nil || !strings.Contains(err.Error(), "opencode-zen/big-pickle presence") {
		t.Fatalf("contradictory explicit classification accepted: %v", err)
	}
}

func TestModelsDevAllDownloadFailuresAreDistinguishedFromRejectedChanges(t *testing.T) {
	var calls []string
	client := testModelsDevMirrors(t, nil, &calls)
	_, err := fetchModelsDevMirrors(context.Background(), client, modelsDevTestURLs, nil, time.Now())
	if err == nil || !strings.Contains(err.Error(), "all models.dev catalog downloads failed") || strings.Contains(err.Error(), "update rejected") || len(calls) != len(modelsDevTestURLs) {
		t.Fatalf("incorrect download failure: %v %v", calls, err)
	}
}

func TestModelsDevRecentlyReplacedMirrorCannotConfirmAFieldRollback(t *testing.T) {
	old := testModelsDevCatalog(t, modelsDevTestSource)
	currentSource := rewriteModelsDevSource(t, modelsDevTestSource, func(providers map[string]map[string]modelsDevModel) {
		row := providers["opencode"]["big-pickle"]
		row.Limit.Context = 50000
		providers["opencode"][row.ID] = row
	})
	current := testModelsDevCatalog(t, currentSource)
	current.History = []modelsDevRevision{{Revision: old.Revision, ReplacedAt: time.Now().Add(-time.Hour)}}
	// The candidate has an unseen digest, but its limit matches a known old
	// mirror. That old mirror must not become evidence for the reverted value.
	candidate := rewriteModelsDevSource(t, modelsDevTestSource, func(providers map[string]map[string]modelsDevModel) {
		row := providers["opencode"]["big-pickle"]
		row.Name = "A different name makes this an unseen catalog revision"
		providers["opencode"][row.ID] = row
	})
	var calls []string
	client := testModelsDevMirrors(t, map[string]string{modelsDevCatalogURL: candidate, stencilCatalogURL: modelsDevTestSource}, &calls)
	_, err := fetchModelsDevMirrors(context.Background(), client, []string{modelsDevCatalogURL, stencilCatalogURL}, current, time.Now())
	if err == nil || !strings.Contains(err.Error(), "big-pickle contextWindow") || !strings.Contains(err.Error(), "recently replaced") {
		t.Fatalf("a recently replaced mirror confirmed rollback: %v", err)
	}
}

func TestModelsDevBulkConfirmationComparesOnlyTheAffectedFields(t *testing.T) {
	before, after, witness := map[string]OMPModel{}, map[string]OMPModel{}, map[string]OMPModel{}
	for i := range 40 {
		id := strings.Repeat("x", i+1)
		row := OMPModel{Name: "Original", API: "openai-completions", Input: []string{"text"}}
		before[id] = row
		if i < 10 {
			row.Name = "Updated"
		}
		after[id], witness[id] = row, row
		if i == 39 {
			row.Name = "Unrelated correction only on the witness"
			witness[id] = row
		}
	}
	current := &modelsDevCatalog{ModelCount: 40, Providers: map[string]map[string]OMPModel{"custom": before}}
	candidate := &modelsDevCatalog{ModelCount: 40, Providers: map[string]map[string]OMPModel{"custom": after}}
	other := &modelsDevCatalog{ModelCount: 40, Providers: map[string]map[string]OMPModel{"custom": witness}}
	changes := modelsDevChangesToConfirm(current, candidate)
	if len(changes) != 10 {
		t.Fatalf("bulk changes were not individually identified: %v", changes)
	}
	if err := confirmModelsDevChanges(changes, map[string]*modelsDevCatalog{stencilCatalogURL: other}); err != nil {
		t.Fatalf("unrelated change blocked bulk confirmation: %v", err)
	}
	row := witness["x"]
	row.Name = "Conflicting name"
	witness["x"] = row
	if err := confirmModelsDevChanges(changes, map[string]*modelsDevCatalog{stencilCatalogURL: other}); err == nil || !strings.Contains(err.Error(), "custom/x name") {
		t.Fatalf("a conflicting bulk field was accepted: %v", err)
	}
}
