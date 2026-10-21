package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	modelcfg "vista/internal/model"
)

func TestCredentialRefreshAcrossTurnsAndConcurrentTasks(t *testing.T) {
	var exchanges atomic.Int32
	var requests atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/ai/third_party_agents/direct_access":
			if r.Header.Get("Authorization") != "Bearer long-lived-pat" {
				t.Error("long-lived credential lost")
			}
			n := exchanges.Add(1)
			fmt.Fprintf(w, `{"token":"temporary-%d","headers":{"X-Gitlab-Instance-Id":"42"}}`, n)
		case "/chat/completions":
			if r.Header.Get("Authorization") != "Bearer temporary-2" {
				t.Error("stale credential on model request")
			}
			requests.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	source := modelcfg.Model{Provider: "openai-chat", ProviderID: "gitlab", ID: "model", BaseURL: server.URL, APIKey: "long-lived-pat", GitLabInstanceURL: server.URL}
	var cache CredentialCache
	first, _, err := cache.Resolve(context.Background(), source, "saved")
	if err != nil || first.APIKey != "temporary-1" || first.GitLabInstanceURL != "" || first.CredentialRef == "" {
		t.Fatalf("initial exchange failed: %v", err)
	}
	entry := cache.entries["saved"]
	<-entry.gate
	entry.refreshAt = time.Now().Add(-time.Second)
	entry.gate <- struct{}{}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, _, err := cache.Resolve(context.Background(), source, "saved")
			if err != nil || m.APIKey != "temporary-2" {
				t.Errorf("renewal: %v", err)
			}
		}()
	}
	wg.Wait()
	if exchanges.Load() != 2 {
		t.Fatalf("concurrent tasks performed %d exchanges", exchanges.Load())
	}
	first.ReasoningEffort, first.ReasoningDisabled = "high", true
	ctx := WithCredentialResolver(context.Background(), func(ctx context.Context, request modelcfg.Model) (modelcfg.Model, error) {
		fresh, _, err := cache.Resolve(ctx, source, request.Key)
		return fresh, err
	})
	fresh, err := refreshRequestCredentials(ctx, first)
	if err != nil || !fresh.ReasoningDisabled || fresh.ReasoningEffort != "high" || fresh.ID != first.ID || fresh.BaseURL != first.BaseURL {
		t.Fatal("renewal changed request controls")
	}
	if _, err := ModelTurn(ctx, first, []ChatMessage{{Role: "user", Content: "hello"}}, nil); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatal("refreshed turn not sent")
	}
	changed := source
	changed.APIKey = "new-pat"
	if CredentialReference(changed) == CredentialReference(source) {
		t.Fatal("credential edit reused reference")
	}
}

func TestCredentialRefreshCancellationAndExplicitTokens(t *testing.T) {
	var cache CredentialCache
	source := modelcfg.Model{Provider: "bedrock", ProviderID: "amazon-bedrock", APIKey: "explicit-bearer"}
	got, _, err := cache.Resolve(context.Background(), source, "fixed")
	if err != nil || got.CredentialRef != "" || got.APIKey != source.APIKey {
		t.Fatal("explicit token treated as renewable")
	}
	source = modelcfg.Model{Key: "adc", Provider: "vertex-gemini", ProviderID: "google-vertex"}
	ref := CredentialReference(source)
	cache.entries = map[string]*credentialEntry{"adc": {ref: ref, gate: make(chan struct{}, 1)}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := cache.Resolve(ctx, source, "adc"); err != context.Canceled {
		t.Fatalf("blocked renewal ignored cancellation: %v", err)
	}
	if _, err := refreshRequestCredentials(context.Background(), modelcfg.Model{CredentialRef: strings.Repeat("a", 64)}); err == nil {
		t.Fatal("worker sent a stale credential without resolver")
	}
}

func TestGitLabCredentialExpiryFormats(t *testing.T) {
	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	for _, reported := range []string{fmt.Sprint(expiry.Unix()), fmt.Sprintf("%q", expiry.Format(time.RFC3339)), `null`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"token":"temporary","expires_at":%s}`, reported)
		}))
		model, _, err := ResolveRunCredentials(context.Background(), modelcfg.Model{ProviderID: "gitlab", APIKey: "pat", GitLabInstanceURL: server.URL}, "key")
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
		if reported == `null` {
			if !model.CredentialExpiresAt.IsZero() {
				t.Fatal("invented expiry")
			}
		} else if !model.CredentialExpiresAt.Equal(expiry) {
			t.Fatal("expiry was lost")
		}
	}
}
