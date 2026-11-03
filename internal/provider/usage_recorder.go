package provider

import (
	"context"

	"github.com/google/uuid"
)

type usageRecorderKey struct{}

// WithUsageRecorder observes completed API attempts, including failures with
// partial usage. Nil means the upstream did not supply usable usage data.
// Cache reads are already in input, and reasoning is already in output.
func WithUsageRecorder(ctx context.Context, record func(string, *int64)) context.Context {
	return context.WithValue(ctx, usageRecorderKey{}, record)
}

func recordUsage(ctx context.Context, usage *ModelUsage) {
	record, ok := ctx.Value(usageRecorderKey{}).(func(string, *int64))
	if !ok {
		return
	}
	var total *int64
	if usage != nil && usage.InputTokens >= 0 && usage.InputTokens <= 1_000_000_000 && usage.OutputTokens >= 0 && usage.OutputTokens <= 1_000_000_000 {
		tokens := usage.InputTokens + usage.OutputTokens
		total = &tokens
	}
	record(uuid.NewString(), total)
}
