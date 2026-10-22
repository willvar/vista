package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
)

type ompTestTransport func(*http.Request) (*http.Response, error)

func (f ompTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const ompTestSource = `{"opencode-zen":{"vista-catalog-fixture":{"id":"vista-catalog-fixture","provider":"opencode-zen","name":"Updated model","api":"openai-responses","input":["text","image"],"contextWindow":100000,"maxTokens":8192,"reasoning":true,"supportsTools":true,"thinking":{"mode":"effort","efforts":["low","max"],"effortMap":{"max":"xhigh"}},"headers":{"discard":"runtime-only-field"},"cost":{"input":10}}}}`

func ompDownloadClient(t *testing.T, revision, source string) *http.Client {
	t.Helper()
	return &http.Client{Transport: ompTestTransport(func(r *http.Request) (*http.Response, error) {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("catalog update included a model credential")
		}
		body := source
		switch r.URL.Host {
		case "api.github.com":
			if r.URL.String() != ompCatalogVersionURL {
				t.Errorf("unexpected version endpoint: %s", r.URL)
			}
			body = fmt.Sprintf(`{"sha":%q}`, revision)
		case "raw.githubusercontent.com", "cdn.jsdelivr.net":
			if r.URL.String() != ompCatalogRawURL && r.URL.String() != ompCatalogCDNURL {
				t.Errorf("unexpected model endpoint: %s", r.URL)
			}
		default:
			t.Errorf("unexpected catalog host: %s", r.URL.Host)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
}

func TestOMPCatalogDownloadPersistenceAndInvalidCache(t *testing.T) {
	original := CurrentOMPCatalog()
	t.Cleanup(original.Activate)
	revision := ompSourceRevision([]byte(ompTestSource))
	next, err := FetchOMPCatalog(context.Background(), ompDownloadClient(t, revision, ompTestSource))
	if err != nil {
		t.Fatal(err)
	}
	if CurrentOMPCatalog() != original || next.Info().ModelCount != 1 || next.Info().ProviderCount != 1 {
		t.Fatal("staged download changed active metadata or lost model counts")
	}
	if ids := next.TextModelIDs("opencode"); len(ids) != 1 || ids[0] != "vista-catalog-fixture" {
		t.Fatalf("staged Zen alias routes: %v", ids)
	}
	dir := t.TempDir()
	if err := next.Save(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(OMPCatalogPath(dir))
	if err != nil || strings.Contains(string(data), "runtime-only-field") || strings.Contains(string(data), `"cost"`) {
		t.Fatalf("cache retained transport or pricing fields: %v", err)
	}
	if err := LoadOMPCatalog(dir); err != nil {
		t.Fatal(err)
	}
	if name := ModelDisplayNameFor("opencode", "vista-catalog-fixture"); name != "Updated model" {
		t.Fatalf("local catalog was not activated: %q", name)
	}
	model, ok := OMPModelMetadata("opencode", "vista-catalog-fixture")
	if !ok || model.Thinking.EffortMap["max"] != "xhigh" || len(model.Input) != 2 || OMPCatalogRevision() != revision {
		t.Fatalf("loaded metadata: %+v", model)
	}
	original.Activate() // A new process must load the persisted update again.
	if err := LoadOMPCatalog(dir); err != nil || OMPCatalogRevision() != revision {
		t.Fatalf("reload catalog after restart: %v", err)
	}
	if err := os.WriteFile(OMPCatalogPath(dir), []byte(`{"source":"broken"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := LoadOMPCatalog(dir); err == nil || OMPCatalogRevision() != revision {
		t.Fatal("invalid local catalog replaced the active good snapshot")
	}
}

func TestOMPCatalogDownloadRejectsInvalidMetadata(t *testing.T) {
	original := CurrentOMPCatalog()
	for name, source := range map[string]string{
		"empty":         `{}`,
		"truncated":     `{"openai":`,
		"identity":      strings.Replace(ompTestSource, `"id":"vista-catalog-fixture"`, `"id":"another-id"`, 1),
		"effort":        strings.Replace(ompTestSource, `"low","max"`, `"low","invented"`, 1),
		"modality":      strings.Replace(ompTestSource, `"text","image"`, `"text","unknown"`, 1),
		"negativeLimit": strings.Replace(ompTestSource, `"maxTokens":8192`, `"maxTokens":-1`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := FetchOMPCatalog(context.Background(), ompDownloadClient(t, ompSourceRevision([]byte(source)), source))
			if err == nil || CurrentOMPCatalog() != original {
				t.Fatalf("bad download changed metadata: %v", err)
			}
		})
	}
	if _, err := FetchOMPCatalog(context.Background(), ompDownloadClient(t, "../main", ompTestSource)); err == nil {
		t.Fatal("invalid commit accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FetchOMPCatalog(ctx, ompDownloadClient(t, strings.Repeat("d", 40), ompTestSource)); err == nil {
		t.Fatal("cancelled download succeeded")
	}
	client := &http.Client{Transport: ompTestTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader("rate limited"))}, nil
	})}
	if _, err := FetchOMPCatalog(context.Background(), client); err == nil || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("rate limit not reported: %v", err)
	}
}

func TestOMPCatalogSameRevisionAvoidsModelDownload(t *testing.T) {
	current := CurrentOMPCatalog()
	next, err := FetchOMPCatalog(context.Background(), ompDownloadClient(t, current.Info().Revision, "not valid JSON"))
	if err != nil || next != current {
		t.Fatalf("current revision was downloaded again: %v", err)
	}
}

func TestOMPCatalogRejectsStaleMirrorBytes(t *testing.T) {
	newRevision := ompSourceRevision([]byte(ompTestSource))
	stale := strings.Replace(ompTestSource, "Updated model", "Old model", 1)
	client := ompDownloadClient(t, newRevision, stale)
	if _, err := FetchOMPCatalog(context.Background(), client); err == nil || !strings.Contains(err.Error(), "authoritative file SHA") {
		t.Fatalf("stale mirror replaced the catalog: %v", err)
	}
	for _, healthyHost := range []string{"raw.githubusercontent.com", "cdn.jsdelivr.net"} {
		t.Run(healthyHost, func(t *testing.T) {
			base := ompDownloadClient(t, newRevision, ompTestSource)
			client := &http.Client{Transport: ompTestTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.github.com" && r.URL.Host != healthyHost {
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(stale))}, nil
				}
				return base.Transport.RoundTrip(r)
			})}
			next, err := FetchOMPCatalog(context.Background(), client)
			if err != nil || next.Info().Revision != newRevision {
				t.Fatalf("valid mirror was not used: %v", err)
			}
			if name, _ := next.model("opencode", "vista-catalog-fixture"); name.Name != "Updated model" {
				t.Fatal("old mirror content was accepted")
			}
		})
	}
}

func TestOMPCatalogActivationIsSafeForConcurrentReaders(t *testing.T) {
	original := CurrentOMPCatalog()
	t.Cleanup(original.Activate)
	next, err := FetchOMPCatalog(context.Background(), ompDownloadClient(t, ompSourceRevision([]byte(ompTestSource)), ompTestSource))
	if err != nil {
		t.Fatal(err)
	}
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for range 300 {
				name := ModelDisplayNameFor("custom", "vista-catalog-fixture")
				if name != "" && name != "Updated model" {
					t.Errorf("partially published metadata: %q", name)
				}
				_ = ModelReasoningOptionsFor("custom", "vista-catalog-fixture")
				_ = OMPModelIDs("opencode")
			}
		})
	}
	for range 100 {
		next.Activate()
		original.Activate()
	}
	readers.Wait()
}

func TestRestoreBundledCatalogDropsDownloadedMetadata(t *testing.T) {
	original := CurrentOMPCatalog()
	t.Cleanup(original.Activate)
	revision := ompSourceRevision([]byte(ompTestSource))
	next, err := FetchOMPCatalog(context.Background(), ompDownloadClient(t, revision, ompTestSource))
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
	if err := RestoreBundledOMPCatalog(); err != nil {
		t.Fatal(err)
	}
	if CurrentOMPCatalog().Info().Revision == revision {
		t.Fatal("reset retained downloaded metadata")
	}
	// A later explicit reload must not be fooled by the pre-reset stat cache.
	if err := LoadOMPCatalog(dir); err != nil || CurrentOMPCatalog().Info().Revision != revision {
		t.Fatalf("catalog could not reload after reset: %v", err)
	}
}
