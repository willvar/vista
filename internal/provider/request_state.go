package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	modelcfg "vista/internal/model"
	"vista/internal/network"
)

// RequestState belongs to one conversation. Neither state is persisted to replay
// history; starting a user turn resets only Codex routing, retaining diagnostics.
type RequestState struct {
	Codex CodexRoutingState
	Cache CacheState
}

type CacheState struct {
	mu        sync.Mutex
	baselines map[string]cacheBaseline
	sequence  uint64
}

type cacheBaseline struct {
	fingerprint cacheFingerprint
	cached      *int64
	sequence    uint64
}

type requestStateKey struct{}

func WithRequestState(ctx context.Context, state *RequestState) context.Context {
	return context.WithValue(ctx, requestStateKey{}, state)
}

func RequestStateFromContext(ctx context.Context) *RequestState {
	state, _ := ctx.Value(requestStateKey{}).(*RequestState)
	return state
}

func (s *RequestState) Reset() {
	if s != nil {
		s.Codex.Reset()
	}
}

func (s *CacheState) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.baselines, s.sequence = nil, 0
}

func requestOwner(ctx context.Context, model modelcfg.Model, request *http.Request) string {
	// Store a digest of actual routing/configuration and credentials, never their
	// raw values. An edited proxy with the same ID is a different route.
	policy := ""
	if model.Network != nil {
		policy = network.Fingerprint(*model.Network)
	} else if network.HasPolicy(ctx) {
		if p, err := network.FromContext(ctx); err == nil {
			policy = network.Fingerprint(p)
		}
	}
	headers, _ := json.Marshal(model.Headers)
	gitlabHeaders, _ := json.Marshal(model.GitLabHeaders)
	encoded := strings.Join([]string{
		request.URL.String(), model.ConnectionID, model.ProxyID, policy,
		model.Provider, model.ProviderID, model.ID, model.WireID, model.SessionID, string(model.ResponsesRoute),
		model.CredentialRef, model.APIKey, model.CodexCredentialPath, model.Authentication, string(headers), string(gitlabHeaders),
		model.AWSAccessID, model.AWSSecret, model.AWSSession, model.AppCode,
		model.OpenAIOrganization, model.OpenAIProject, model.ProjectID, model.DeploymentID, model.ResourceGroup,
		request.Header.Get("Chatgpt-Account-Id"),
	}, "\x00")
	sum := sha256.Sum256([]byte(encoded))
	return hex.EncodeToString(sum[:])
}
