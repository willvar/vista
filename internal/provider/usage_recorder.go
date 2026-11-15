package provider

import (
	"context"

	"github.com/google/uuid"
)

type usageRecorderKey struct{}

func validReportedTokens(count int64) bool {
	const maxReportedTokens = 1_000_000_000
	return count >= 0 && count <= maxReportedTokens
}

// ValidModelUsage validates normalized usage in adapters and at the Worker
// boundary. Input already includes both cache reads and cache writes.
func ValidModelUsage(usage ModelUsage) bool {
	return validReportedTokens(usage.InputTokens) && validReportedTokens(usage.OutputTokens) && validReportedTokens(usage.CacheReadTokens) && validReportedTokens(usage.ReasoningTokens) && usage.CacheReadTokens <= usage.InputTokens && (usage.CacheWriteTokens == nil || validReportedTokens(*usage.CacheWriteTokens) && *usage.CacheWriteTokens <= usage.InputTokens-usage.CacheReadTokens)
}

// WithUsageRecorder observes completed API attempts, including failures with
// partial usage. Failed attempts without usable usage are excluded. Nil means
// a successful attempt did not supply usable usage data.
// Cache reads are already in input, and reasoning is already in output.
func WithUsageRecorder(ctx context.Context, record func(string, *int64)) context.Context {
	return context.WithValue(ctx, usageRecorderKey{}, record)
}

func recordUsage(ctx context.Context, usage *ModelUsage, requestErr error) {
	record, ok := ctx.Value(usageRecorderKey{}).(func(string, *int64))
	if !ok {
		return
	}
	var total *int64
	if usage != nil && ValidModelUsage(*usage) {
		tokens := usage.InputTokens + usage.OutputTokens
		total = &tokens
	}
	if requestErr != nil && total == nil {
		return
	}
	record(uuid.NewString(), total)
}

// Stream errors must preserve usage already reported by the upstream without
// turning partial text or tool calls into a completed response.
func retainUsage(result *Turn, usage **ModelUsage) {
	if result.Usage == nil && *usage != nil && ValidModelUsage(**usage) {
		result.Usage = *usage
	}
}
