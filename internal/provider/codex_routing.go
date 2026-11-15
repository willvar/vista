package provider

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
	modelcfg "vista/internal/model"
)

// CodexRoutingState owns only volatile per-turn routing, independently of cache diagnostics.
type CodexRoutingState struct {
	mu                 sync.Mutex
	owner, token, turn string
	epoch              uint64
}

func (s *CodexRoutingState) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token, s.turn = "", ""
	s.epoch++
}

func codexRouting(model modelcfg.Model) bool {
	return model.ResponsesRoute == modelcfg.ResponsesRouteCodex || model.ResponsesRoute == modelcfg.ResponsesRouteCodexCompatible
}

// prepareCodexRouting returns a response observer bound to this owner and
// turn generation. Late responses cannot seed a different owner's fresh turn.
func prepareCodexRouting(ctx context.Context, model modelcfg.Model, request *http.Request) (func(*http.Response), error) {
	noop := func(*http.Response) {}
	if !codexRouting(model) {
		return noop, nil
	}
	for name := range model.Headers {
		switch strings.ToLower(name) {
		case "session-id", "session_id", "x-codex-turn-state", "x-client-request-id":
			return nil, errors.New("Codex routing headers are session-owned; remove static routing header overrides")
		}
	}
	if model.SessionID == "" {
		return noop, nil
	}
	key := stableCacheKey(model.SessionID)
	request.Header.Set("session-id", key)
	requestState := RequestStateFromContext(ctx)
	if requestState == nil {
		return noop, nil
	}
	state := &requestState.Codex
	owner := requestOwner(ctx, model, request)
	state.mu.Lock()
	if state.owner != owner {
		state.owner, state.token, state.turn = owner, "", ""
		state.epoch++
	}
	if state.turn == "" {
		state.turn = uuid.NewString()
	}
	request.Header.Set("x-client-request-id", state.turn)
	request.Header.Del("x-codex-turn-state")
	if state.token != "" {
		request.Header.Set("x-codex-turn-state", state.token)
	}
	epoch := state.epoch
	state.mu.Unlock()
	return func(response *http.Response) {
		if response.StatusCode != http.StatusOK {
			return
		}
		token := response.Header.Get("x-codex-turn-state")
		if token == "" || len(token) > 8192 || strings.ContainsAny(token, "\r\n") {
			return
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.owner == owner && state.epoch == epoch && state.token == "" {
			state.token = token
		}
	}, nil
}
