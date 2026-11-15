package modeltrace

import "context"

// CacheObservation contains bounded structural evidence, never prompt text,
// credentials, opaque replay data or routing tokens. Nil counters mean unknown.
type CacheObservation struct {
	InputTokens       *int64 `json:"inputTokens,omitempty"`
	CachedTokens      *int64 `json:"cachedTokens,omitempty"`
	CacheWriteTokens  *int64 `json:"cacheWriteTokens,omitempty"`
	PrefixChange      string `json:"prefixChange"`
	FirstChangedInput *int   `json:"firstChangedInput,omitempty"`
	CachedDrop        bool   `json:"cachedDrop"`
	RequestBytes      int    `json:"requestBytes"`
	InputItems        int    `json:"inputItems"`
	ComparisonBounded bool   `json:"comparisonBounded"`
	InstructionsHash  string `json:"instructionsHash"`
	ToolsHash         string `json:"toolsHash"`
}

func SetCacheObservation(ctx context.Context, observation CacheObservation) {
	if capture, _ := ctx.Value(captureKey{}).(*Capture); capture != nil {
		capture.mu.Lock()
		defer capture.mu.Unlock()
		if !capture.finished {
			capture.record.Cache = &observation
		}
	}
}
