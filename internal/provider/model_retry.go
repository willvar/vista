package provider

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/url"
	"strings"
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/modeltrace"
)

const MaxModelRetries = 3
const maxModelRetryWait = time.Minute

// ModelRetry describes recovery of one model request, never replay of tools.
type ModelRetry struct {
	Status     string
	Attempt    int
	MaxRetries int
	RetryAt    time.Time
	Error      string
}

type modelRetryObserverKey struct{}
type modelRetryDelayKey struct{} // A per-context clock scale for isolated tests.

func WithModelRetryObserver(ctx context.Context, observe func(ModelRetry) error) context.Context {
	return context.WithValue(ctx, modelRetryObserverKey{}, observe)
}

func observeModelRetry(ctx context.Context, state ModelRetry) error {
	if observe, ok := ctx.Value(modelRetryObserverKey{}).(func(ModelRetry) error); ok {
		return observe(state)
	}
	return nil
}

// Only request rejection and pre-response transport errors are eligible.
// Stream/protocol failures are terminal: adapters may already have consumed
// server tools or other output that their error return cannot reconstruct.
func modelRetryDelay(ctx context.Context, err error, attempt int) (time.Duration, bool) {
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) || IsContextOverflow(err) {
		return 0, false
	}
	var failure *ModelHTTPError
	var hint time.Duration
	if errors.As(err, &failure) {
		switch strings.ToLower(failure.Code) {
		case "insufficient_quota", "quota_exceeded", "billing_hard_limit_reached", "payment_required", "invalid_api_key", "modeldeprecated", "model_not_found", "unsupported_endpoint":
			return 0, false
		}
		switch failure.StatusCode {
		case 408, 429, 500, 502, 503, 504, 529:
			hint = failure.RetryAfter
		default:
			return 0, false
		}
	} else {
		var requestError *url.Error
		if !errors.As(err, &requestError) {
			return 0, false
		}
		var networkError net.Error
		if !errors.As(requestError.Err, &networkError) && !errors.Is(requestError.Err, io.EOF) && !errors.Is(requestError.Err, io.ErrUnexpectedEOF) {
			return 0, false
		}
	}
	base := 2 * time.Second
	if scale, ok := ctx.Value(modelRetryDelayKey{}).(time.Duration); ok {
		base = scale
	}
	delay := base * time.Duration(1<<min(max(attempt-1, 0), MaxModelRetries-1))
	// Jitter cannot shorten the server's minimum wait.
	delay = time.Duration(float64(delay) * (0.8 + rand.Float64()*0.4))
	delay = max(delay, hint)
	return delay, delay <= maxModelRetryWait
}

func retryModelTurn(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any, adapter protocolAdapter, onText, onReasoning func(string) error) (Turn, error) {
	providerID := model.ProviderID
	if providerID == "" {
		providerID = model.Provider
	}
	state := ModelRetry{MaxRetries: MaxModelRetries}
	for {
		if err := ctx.Err(); err != nil {
			return Turn{}, err
		}
		attemptCtx, capture := modeltrace.Start(ctx, providerID, model.Provider, model.ID, modelSecrets(model))
		streamed := false
		wrap := func(callback func(string) error) func(string) error {
			if callback == nil {
				return nil
			}
			return func(fragment string) error {
				streamed = streamed || fragment != ""
				return callback(fragment)
			}
		}
		turn, err := adapter.turn(attemptCtx, model, messages, schemas, wrap(onText), wrap(onReasoning))
		recordUsage(attemptCtx, turn.Usage, err)
		capture.Finish(turn.FinishReason, err)
		err = modelErrorForDisplay(err, model)
		if ctx.Err() != nil {
			return turn, ctx.Err()
		}
		delay, retryable := modelRetryDelay(ctx, err, state.Attempt+1)
		if err == nil || !retryable || streamed || turn.Text != "" || len(turn.Calls) > 0 || len(turn.HostedCalls) > 0 || turn.Usage != nil || state.Attempt >= MaxModelRetries {
			if state.Attempt > 0 {
				state.Status, state.RetryAt = "succeeded", time.Time{}
				if err != nil {
					state.Status, state.Error = "failed", err.Error()
				}
				if observeErr := observeModelRetry(ctx, state); observeErr != nil {
					return turn, observeErr
				}
			}
			return turn, err
		}
		state.Attempt++
		state.Status, state.RetryAt, state.Error = "waiting", time.Now().UTC().Add(delay), err.Error()
		if err := observeModelRetry(ctx, state); err != nil {
			return turn, err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return turn, ctx.Err()
		case <-timer.C:
		}
		state.Status, state.RetryAt = "requesting", time.Time{}
		if err := observeModelRetry(ctx, state); err != nil {
			return turn, err
		}
	}
}
