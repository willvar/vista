package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"vista/internal/modeltrace"
)

const cacheFingerprintItems = 512
const cacheFingerprintBytes = 16 << 20

type cacheFingerprint struct {
	requestedTier, actualTier string
	route                     [32]byte
	instructions              [32]byte
	tools                     [32]byte
	parameters                [32]byte
	items                     [][32]byte
	count                     int
	bounded                   bool
}

func fingerprintPrompt(body map[string]any) cacheFingerprint {
	body = cacheProjection(body)
	var result cacheFingerprint
	remaining := cacheFingerprintBytes
	hash := func(value any) [32]byte {
		encoded, _ := json.Marshal(value)
		if len(encoded) > remaining {
			encoded = encoded[:remaining]
			result.bounded = true
		}
		remaining -= len(encoded)
		return sha256.Sum256(encoded)
	}
	result.instructions = hash(body["instructions"])
	result.tools = hash(body["tools"])
	parameters := make(map[string]any, len(body))
	for key, value := range body {
		if key != "instructions" && key != "tools" && key != "input" {
			parameters[key] = value
		}
	}
	result.parameters = hash(parameters)
	if items, ok := body["input"].([]any); ok {
		result.count = len(items)
		for _, item := range items[:min(len(items), cacheFingerprintItems)] {
			result.items = append(result.items, hash(item))
		}
		result.bounded = result.bounded || len(items) > cacheFingerprintItems
	}
	return result
}

func compareCachePrefixes(previous, current *cacheFingerprint) (string, *int) {
	if previous == nil {
		return "new-series", nil
	}
	if previous.route != current.route {
		return "route", nil
	}
	if previous.instructions != current.instructions {
		return "instructions", nil
	}
	if previous.tools != current.tools {
		return "tools", nil
	}
	if previous.parameters != current.parameters {
		return "parameters", nil
	}
	for i := 0; i < min(len(previous.items), len(current.items)); i++ {
		if previous.items[i] != current.items[i] {
			return "history", &i
		}
	}
	if previous.bounded || current.bounded {
		return "unchecked-tail", nil
	}
	if current.count < previous.count {
		index := current.count
		return "history-shortened", &index
	}
	if current.count > previous.count {
		return "append-only", nil
	}
	return "unchanged", nil
}

func observeCache(ctx context.Context, body map[string]any, requestBytes int, usage *ModelUsage, route ...string) modeltrace.CacheObservation {
	return observeCacheFingerprint(ctx, fingerprintPrompt(body), requestBytes, usage, route...)
}

func observeCacheFingerprint(ctx context.Context, fingerprint cacheFingerprint, requestBytes int, usage *ModelUsage, route ...string) modeltrace.CacheObservation {
	if len(route) > 0 {
		fingerprint.route = sha256.Sum256([]byte(route[0]))
	}
	observation := modeltrace.CacheObservation{
		PrefixChange: "new-series", RequestBytes: requestBytes, InputItems: fingerprint.count,
		RequestedProcessingTier: fingerprint.requestedTier, ActualProcessingTier: fingerprint.actualTier,
		ComparisonBounded: fingerprint.bounded,
		InstructionsHash:  hex.EncodeToString(fingerprint.instructions[:]), ToolsHash: hex.EncodeToString(fingerprint.tools[:]),
	}
	if usage != nil && ValidModelUsage(*usage) {
		input := usage.InputTokens
		observation.InputTokens = &input
		observation.CacheWriteTokens = usage.CacheWriteTokens
		if usage.CacheReadKnown != nil && *usage.CacheReadKnown {
			cached := usage.CacheReadTokens
			observation.CachedTokens = &cached
		}
	}
	if requestState := RequestStateFromContext(ctx); requestState != nil {
		state := &requestState.Cache
		key := ""
		if len(route) > 0 {
			key = route[0]
		}
		state.mu.Lock()
		if state.baselines == nil {
			state.baselines = make(map[string]cacheBaseline)
		}
		previous, exists := state.baselines[key]
		if exists {
			observation.ProcessingTierChanged = previous.fingerprint.requestedTier != fingerprint.requestedTier || previous.fingerprint.actualTier != "" && fingerprint.actualTier != "" && previous.fingerprint.actualTier != fingerprint.actualTier
			observation.PrefixChange, observation.FirstChangedInput = compareCachePrefixes(&previous.fingerprint, &fingerprint)
		}
		if observation.CachedTokens != nil {
			observation.CachedDrop = previous.cached != nil && *observation.CachedTokens < *previous.cached
		}
		if !exists || previous.cached == nil || observation.CachedTokens != nil {
			// Keep metadata bounded even if endpoints/credentials change repeatedly.
			if !exists && len(state.baselines) >= 16 {
				var oldest string
				var sequence uint64 = ^uint64(0)
				for owner, entry := range state.baselines {
					if entry.sequence < sequence {
						oldest, sequence = owner, entry.sequence
					}
				}
				delete(state.baselines, oldest)
			}
			state.sequence++
			state.baselines[key] = cacheBaseline{fingerprint: fingerprint, cached: observation.CachedTokens, sequence: state.sequence}
		}
		state.mu.Unlock()
	}
	modeltrace.SetCacheObservation(ctx, observation)
	return observation
}
