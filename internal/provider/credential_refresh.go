package provider

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	modelcfg "vista/internal/model"
)

type credentialResolverKey struct{}
type CredentialResolver func(context.Context, modelcfg.Model) (modelcfg.Model, error)

// WithCredentialResolver keeps long-lived cloud credentials in the Server.
// A Worker asks for fresh request credentials over IPC, including detached work.
func WithCredentialResolver(ctx context.Context, resolve CredentialResolver) context.Context {
	return context.WithValue(ctx, credentialResolverKey{}, resolve)
}

func refreshRequestCredentials(ctx context.Context, model modelcfg.Model) (modelcfg.Model, error) {
	if model.CredentialRef == "" {
		return model, nil
	}
	resolve, _ := ctx.Value(credentialResolverKey{}).(CredentialResolver)
	if resolve == nil {
		return modelcfg.Model{}, errors.New("model credential resolver unavailable")
	}
	fresh, err := resolve(ctx, model)
	if err != nil {
		return modelcfg.Model{}, err
	}
	// Preserve the request's model, reasoning, limits and purpose. Only material
	// produced by a credential exchange may change between requests.
	model.APIKey, model.AWSAccessID, model.AWSSecret, model.AWSSession = fresh.APIKey, fresh.AWSAccessID, fresh.AWSSecret, fresh.AWSSession
	model.GitLabHeaders = fresh.GitLabHeaders
	model.CredentialExpiresAt = fresh.CredentialExpiresAt
	return model, nil
}

func renewableCredentials(model modelcfg.Model) bool {
	switch model.ProviderID {
	case "google-vertex":
		return model.APIKey == ""
	case "gitlab", "watsonx", "sap-ai-core":
		return true
	}
	switch model.Provider {
	case "bedrock", "bedrock-mantle-chat", "bedrock-mantle-responses":
		return model.APIKey == ""
	}
	return false
}

// CredentialReference identifies the saved credential source, before exchange
// and session reasoning overrides. An edit/disconnect invalidates old references.
func CredentialReference(source modelcfg.Model) string {
	data, _ := json.Marshal(source)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

type credentialEntry struct {
	ref       string
	gate      chan struct{}
	model     modelcfg.Model
	refreshAt time.Time
}

// CredentialCache coalesces exchanges per source. There is at most one cached
// entry per configured model; secrets are never included in events or history.
type CredentialCache struct {
	mu      sync.Mutex
	entries map[string]*credentialEntry
}

func (c *CredentialCache) Resolve(ctx context.Context, source modelcfg.Model, key string) (modelcfg.Model, string, error) {
	if !renewableCredentials(source) {
		return ResolveRunCredentials(ctx, source, key)
	}
	source.Key = key
	ref := CredentialReference(source)
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]*credentialEntry{}
	}
	entry := c.entries[key]
	if entry == nil || entry.ref != ref {
		entry = &credentialEntry{ref: ref, gate: make(chan struct{}, 1)}
		entry.gate <- struct{}{}
		c.entries[key] = entry
	}
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return modelcfg.Model{}, "", ctx.Err()
	case <-entry.gate:
	}
	defer func() { entry.gate <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return modelcfg.Model{}, "", err
	}
	if time.Now().Before(entry.refreshAt) {
		return entry.model, key, nil
	}
	// Token endpoints and ambient credential chains must not hold a request
	// indefinitely. Providers may impose a shorter timeout themselves.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if source.DeploymentID == "" {
		source.DeploymentID = entry.model.DeploymentID
	}
	resolved, _, err := ResolveRunCredentials(ctx, source, key)
	if err != nil {
		return modelcfg.Model{}, "", err
	}
	if err := ctx.Err(); err != nil {
		return modelcfg.Model{}, "", err
	}
	resolved.Key, resolved.CredentialRef = key, ref
	now := time.Now()
	expiry := resolved.CredentialExpiresAt
	if expiry.IsZero() {
		expiry = now.Add(time.Minute)
	}
	// Keep short-lived test/provider tokens usable while allowing clock skew.
	lead := min(time.Minute, max(time.Duration(0), expiry.Sub(now)/10))
	entry.model, entry.refreshAt = resolved, expiry.Add(-lead)
	return resolved, key, nil
}
