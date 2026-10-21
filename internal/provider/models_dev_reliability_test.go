package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

var modelsDevTestURLs = []string{modelsDevCatalogURL, openCodeModelsCatalogURL, stencilCatalogURL}

func testModelsDevCatalog(t *testing.T, source string) *modelsDevCatalog {
	t.Helper()
	catalog, err := decodeModelsDevCatalog([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func testModelsDevMirrors(t *testing.T, sources map[string]string, calls *[]string) *http.Client {
	t.Helper()
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	bodies := map[string][]byte{}
	for url, source := range sources {
		bodies[url] = []byte(source)
		if url == stencilCatalogURL {
			bodies[url] = encoder.EncodeAll([]byte(source), nil)
		}
	}
	return &http.Client{Transport: ompTestTransport(func(r *http.Request) (*http.Response, error) {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		url := r.URL.String()
		*calls = append(*calls, url)
		if r.Header.Get("Authorization") != "" {
			t.Error("metadata request included credentials")
		}
		body, found := bodies[url]
		status := http.StatusOK
		if !found {
			status = http.StatusServiceUnavailable
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{"Etag": {`"fixture-etag"`}}}, nil
	})}
}

func TestModelsDevOrdinaryChangesNeedOnlyOneAvailableMirror(t *testing.T) {
	current := testModelsDevCatalog(t, modelsDevTestSource)
	updated := strings.Replace(modelsDevTestSource, "Big Pickle", "Renamed Pickle", 1)
	var calls []string
	client := testModelsDevMirrors(t, map[string]string{stencilCatalogURL: updated}, &calls)
	next, err := fetchModelsDevMirrors(context.Background(), client, []string{stencilCatalogURL, modelsDevCatalogURL, openCodeModelsCatalogURL}, current, time.Now())
	if err != nil || next.Revision == current.Revision || len(calls) != 1 {
		t.Fatalf("ordinary correction required another source: calls=%v err=%v", calls, err)
	}
	if len(current.History) != 0 || len(current.ETags) != 0 || len(next.History) != 1 || next.History[0].Revision != current.Revision {
		t.Fatal("staging mutated installed history or omitted the replaced digest")
	}
}

func TestModelsDevHistorySurvivesReloadAndExpiresWithoutAnUpdateCooldown(t *testing.T) {
	original := CurrentOMPCatalog()
	t.Cleanup(original.Activate)
	base, err := FetchModelCatalog(context.Background(), modelsDevDownloadClient(t, original.Info().Revision, "unused", modelsDevTestSource))
	if err != nil {
		t.Fatal(err)
	}
	base.Activate()
	updated := strings.Replace(modelsDevTestSource, "Big Pickle", "Renamed Pickle", 1)
	next, err := FetchModelCatalog(context.Background(), modelsDevDownloadClient(t, base.Info().Revision, "unused", updated))
	if err != nil || next.Info().ModelsDevRevision == base.Info().ModelsDevRevision {
		t.Fatalf("small update was delayed: %v", err)
	}
	dir := t.TempDir()
	if err := next.Save(dir); err != nil {
		t.Fatal(err)
	}
	original.Activate()
	if err := LoadOMPCatalog(dir); err != nil {
		t.Fatal(err)
	}
	installed := CurrentOMPCatalog().data.ModelsDev
	if len(installed.History) != 1 || installed.History[0].Revision != base.Info().ModelsDevRevision {
		t.Fatal("replaced digest disappeared after restart")
	}
	now := time.Now()
	var calls []string
	client := testModelsDevMirrors(t, map[string]string{modelsDevCatalogURL: modelsDevTestSource, stencilCatalogURL: updated}, &calls)
	kept, err := fetchModelsDevMirrors(context.Background(), client, []string{modelsDevCatalogURL, stencilCatalogURL}, installed, now)
	if err != nil || kept.Revision != installed.Revision || len(calls) != 2 {
		t.Fatalf("known old digest replaced installed content: %v %v", calls, err)
	}
	calls = nil
	_, err = fetchModelsDevMirrors(context.Background(), client, []string{modelsDevCatalogURL}, installed, now)
	if err == nil || !strings.Contains(err.Error(), "recently replaced") {
		t.Fatalf("all stale mirrors did not fail: %v", err)
	}
	// Expiration is based on replacement time. A successful unchanged check
	// must not extend it, and an unseen small change has no 72-hour cooldown.
	expired, err := fetchModelsDevMirrors(context.Background(), client, []string{modelsDevCatalogURL}, kept, now.Add(modelsDevHistoryWindow+time.Second))
	if err != nil || expired.Revision != base.Info().ModelsDevRevision {
		t.Fatalf("expired digest was treated as permanently forbidden: %v", err)
	}
}

func TestModelsDevSuspiciousChangesRequireAnIndependentPublisher(t *testing.T) {
	current := testModelsDevCatalog(t, modelsDevTestSource)
	changed := strings.Replace(modelsDevTestSource, `"low","high","max"`, `"low","high"`, 1)
	for _, test := range []struct {
		name       string
		stencil    string
		wantAccept bool
	}{
		{"aliases alone", "", false},
		{"independent agreement", changed, true},
		{"independent disagreement", modelsDevTestSource, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			sources := map[string]string{modelsDevCatalogURL: changed, openCodeModelsCatalogURL: changed}
			if test.stencil != "" {
				sources[stencilCatalogURL] = test.stencil
			}
			var calls []string
			client := testModelsDevMirrors(t, sources, &calls)
			next, err := fetchModelsDevMirrors(context.Background(), client, modelsDevTestURLs, current, time.Now())
			if test.wantAccept {
				if err != nil || next.Revision == current.Revision || !slices.Equal(calls, []string{modelsDevCatalogURL, stencilCatalogURL}) {
					t.Fatalf("independent confirmation failed: calls=%v err=%v", calls, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "could not confirm") {
				t.Fatalf("unconfirmed change reported as success: calls=%v err=%v", calls, err)
			}
			for _, url := range modelsDevTestURLs {
				count := 0
				for _, call := range calls {
					if call == url {
						count++
					}
				}
				if count > 1 {
					t.Errorf("mirror was fetched twice in one check: %s", url)
				}
			}
		})
	}
}

func TestModelsDevConditionalRequestsAndUnchangedCheckKeepContentAge(t *testing.T) {
	var calls []string
	client := testModelsDevMirrors(t, map[string]string{stencilCatalogURL: modelsDevTestSource}, &calls)
	current, err := fetchModelsDevMirrors(context.Background(), client, []string{stencilCatalogURL}, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	current.History = []modelsDevRevision{{strings.Repeat("a", 64), time.Now().Add(-time.Hour)}}
	// Conditional requests must still work after persisting and reloading the
	// accepted metadata; validators never refer to an uninstalled response.
	original := CurrentOMPCatalog()
	t.Cleanup(original.Activate)
	file := original.data
	file.ModelsDev = current
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := decodeOMPCatalog(data)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := snapshot.Save(dir); err != nil {
		t.Fatal(err)
	}
	if err := LoadOMPCatalog(dir); err != nil {
		t.Fatal(err)
	}
	current = CurrentOMPCatalog().data.ModelsDev
	client.Transport = ompTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("If-None-Match") != `"fixture-etag"` {
			t.Fatal("conditional request did not use the accepted source validator")
		}
		return &http.Response{StatusCode: http.StatusNotModified, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	next, err := fetchModelsDevMirrors(context.Background(), client, []string{stencilCatalogURL}, current, time.Now())
	if err != nil || next.Revision != current.Revision || !next.History[0].ReplacedAt.Equal(current.History[0].ReplacedAt) {
		t.Fatalf("304 changed metadata or extended history: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	client.Transport = ompTestTransport(func(*http.Request) (*http.Response, error) {
		cancel()
		return &http.Response{StatusCode: http.StatusNotModified, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if _, err := fetchModelsDevMirrors(ctx, client, []string{stencilCatalogURL}, current, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("late conditional response survived cancellation: %v", err)
	}
}

func TestModelsDevBulkChangeConfirmationExcludesAdditions(t *testing.T) {
	for _, test := range []struct {
		name                     string
		existing, changed, added int
		confirm                  bool
	}{
		{"ordinary additions", 40, 0, 200, false},
		{"few corrections", 40, 9, 0, false},
		{"widespread corrections", 40, 10, 0, true},
		{"small proportion", 100, 10, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, after := map[string]OMPModel{}, map[string]OMPModel{}
			for i := range test.existing + test.added {
				id := fmt.Sprint(i)
				model := OMPModel{Name: "Original", API: "openai-completions", Input: []string{"text"}}
				if i < test.existing {
					before[id] = model
				}
				if i < test.changed {
					model.Name = "Corrected"
				}
				after[id] = model
			}
			current := &modelsDevCatalog{ModelCount: test.existing, Providers: map[string]map[string]OMPModel{"custom": before}}
			candidate := &modelsDevCatalog{ModelCount: len(after), Providers: map[string]map[string]OMPModel{"custom": after}}
			if changes := modelsDevChangesToConfirm(current, candidate); (len(changes) != 0) != test.confirm {
				t.Fatalf("bulk-change decision: %+v", changes)
			}
		})
	}
}

func TestModelsDevRiskChecksAllowCorrectionsAndConfirmLosses(t *testing.T) {
	current := testModelsDevCatalog(t, modelsDevTestSource)
	for _, test := range []struct {
		name    string
		change  func(*modelsDevCatalog)
		confirm bool
	}{
		{"unchanged", func(*modelsDevCatalog) {}, false},
		{"ordinary correction", func(c *modelsDevCatalog) {
			m := c.Providers["opencode-zen"]["big-pickle"]
			m.ContextWindow = 180000
			c.Providers["opencode-zen"]["big-pickle"] = m
		}, false},
		{"provider removed", func(c *modelsDevCatalog) { delete(c.Providers, "opencode-go") }, true},
		{"model removed", func(c *modelsDevCatalog) { delete(c.Providers["opencode-zen"], "big-pickle") }, true},
		{"capability lost", func(c *modelsDevCatalog) {
			m := c.Providers["opencode-zen"]["big-pickle"]
			m.Reasoning = false
			c.Providers["opencode-zen"]["big-pickle"] = m
		}, true},
		{"tools unknown", func(c *modelsDevCatalog) {
			m := c.Providers["opencode-zen"]["big-pickle"]
			m.SupportsTools = nil
			c.Providers["opencode-zen"]["big-pickle"] = m
		}, true},
		{"limit missing", func(c *modelsDevCatalog) {
			m := c.Providers["opencode-zen"]["big-pickle"]
			m.ContextWindow = 0
			c.Providers["opencode-zen"]["big-pickle"] = m
		}, true},
		{"limit doubled", func(c *modelsDevCatalog) {
			m := c.Providers["opencode-zen"]["big-pickle"]
			m.ContextWindow *= 2
			c.Providers["opencode-zen"]["big-pickle"] = m
		}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, _ := json.Marshal(current)
			var candidate modelsDevCatalog
			if err := json.Unmarshal(data, &candidate); err != nil {
				t.Fatal(err)
			}
			test.change(&candidate)
			if changes := modelsDevChangesToConfirm(current, &candidate); (len(changes) != 0) != test.confirm {
				t.Fatalf("unexpected confirmation decision: %+v", changes)
			}
		})
	}
}

func TestModelsDevFiltersSpecializedTextModels(t *testing.T) {
	source := strings.Replace(modelsDevTestSource, `"id":"big-pickle","name":`, `"id":"big-pickle","type":"decision","name":`, 1)
	catalog := testModelsDevCatalog(t, source)
	if _, ok := catalog.Providers["opencode-zen"]["big-pickle"]; ok {
		t.Fatal("a specialized decision model entered the chat catalog")
	}
}
