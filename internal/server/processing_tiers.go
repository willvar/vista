package server

import (
	"context"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	modelcfg "vista/internal/model"
	"vista/internal/network"
	"vista/internal/provider"
)

type processingInventoryEntry struct {
	expires time.Time
	tiers   map[string][]string
}

type processingInventoryCache struct {
	mu       sync.Mutex
	revision uint64
	entries  map[string]processingInventoryEntry
	inflight singleflight.Group
}

func (a *App) processingTierMetadata(ctx context.Context, m modelcfg.Model) modelcfg.Model {
	key := provider.ProcessingInventoryKey(m)
	if key == "" {
		return m
	}
	a.modelMu.RLock()
	revision := a.modelRevision
	a.modelMu.RUnlock()
	cache := &a.processingInventory
	cache.mu.Lock()
	if cache.revision != revision {
		cache.entries = nil
		cache.revision = revision
	}
	if m.ProviderID != "openai-codex" {
		key += ":" + provider.CredentialReference(m)
	}
	key += ":" + m.ProxyID
	entry := cache.entries[key]
	cache.mu.Unlock()
	if time.Now().Before(entry.expires) {
		m.AdvertisedProcessingTiers = entry.tiers[m.ID]
		return m
	}
	// The request owns no mutable client state. One cancelling browser should
	// not abort another browser's lookup; the upstream deadline stays bounded.
	result := cache.inflight.DoChan(a.modelSettingsVersion(revision)+"/"+key, func() (any, error) {
		cache.mu.Lock()
		cached := cache.entries[key]
		same := cache.revision == revision
		cache.mu.Unlock()
		if same && time.Now().Before(cached.expires) {
			return cached.tiers, nil
		}
		work, cancel := context.WithTimeout(network.WithConfigDir(a.ctx, a.configDir), 6*time.Second)
		defer cancel()
		tiers, err := provider.DiscoverProcessingTiers(work, m)
		ttl := 5 * time.Minute
		if err != nil {
			ttl = 10 * time.Second
		}
		cache.mu.Lock()
		if cache.revision == revision {
			if len(cache.entries) >= 256 || cache.entries == nil {
				cache.entries = make(map[string]processingInventoryEntry)
			}
			cache.entries[key] = processingInventoryEntry{expires: time.Now().Add(ttl), tiers: tiers}
		}
		cache.mu.Unlock()
		return tiers, nil
	})
	select {
	case <-ctx.Done():
	case value := <-result:
		a.modelMu.RLock()
		same := a.modelRevision == revision
		a.modelMu.RUnlock()
		if tiers, ok := value.Val.(map[string][]string); same && ok {
			m.AdvertisedProcessingTiers = tiers[m.ID]
		}
	}
	return m
}

func (a *App) modelProcessingTiers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", 405)
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" || len(key) > 512 {
		http.Error(w, "invalid model key", 400)
		return
	}
	model, _, err := a.modelsSnapshot().ResolveEnabled(key)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	model.Key = key
	options := provider.ProcessingOptions(a.processingTierMetadata(r.Context(), model))
	w.Header().Set("Cache-Control", "no-store")
	jsonOut(w, map[string]any{"processingTiers": options})
}
