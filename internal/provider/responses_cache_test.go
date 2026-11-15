package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestResponsesCacheTelemetryDistinguishesUnknownZeroAndWrites(t *testing.T) {
	for _, tc := range []struct {
		name, details string
		known         bool
		cached        int64
		writes        *int64
		invalid       bool
	}{
		{name: "unknown", details: `{}`},
		{name: "measured zero", details: `{"cached_tokens":0}`, known: true},
		{name: "read and write", details: `{"cached_tokens":40,"cache_write_tokens":20}`, known: true, cached: 40, writes: func() *int64 { v := int64(20); return &v }()},
		{name: "invalid sum", details: `{"cached_tokens":80,"cache_write_tokens":30}`, invalid: true},
		{name: "boolean is not a count", details: `{"cached_tokens":true}`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":100,\"output_tokens\":3,\"input_tokens_details\":%s}}}\n\n", tc.details)
			turn, err := parseResponses(strings.NewReader(stream))
			if tc.invalid {
				if err == nil {
					t.Fatal("accepted invalid cache telemetry")
				}
				return
			}
			if err != nil || turn.Usage == nil || turn.Usage.CacheReadKnown == nil || *turn.Usage.CacheReadKnown != tc.known || turn.Usage.CacheReadTokens != tc.cached {
				t.Fatalf("cache telemetry: %+v %v", turn.Usage, err)
			}
			if (turn.Usage.CacheWriteTokens == nil) != (tc.writes == nil) || tc.writes != nil && *turn.Usage.CacheWriteTokens != *tc.writes {
				t.Fatal("cache writes lost their known/unknown state")
			}
		})
	}
}

func TestResponsesCacheObservationPreservesLastMeasuredBaseline(t *testing.T) {
	ctx := WithRequestState(context.Background(), new(RequestState))
	known, unknown := true, false
	body := func(text string, extra bool) map[string]any {
		items := []any{map[string]any{"role": "user", "content": "private fixture content"}}
		if extra {
			items = append(items, map[string]any{"role": "user", "content": "next"})
		}
		return map[string]any{"model": "fixture", "instructions": text, "tools": []any{}, "input": items}
	}
	first := observeCache(ctx, body("stable policy", false), 123, &ModelUsage{InputTokens: 100, CacheReadTokens: 80, CacheReadKnown: &known})
	if first.PrefixChange != "new-series" || first.CachedDrop {
		t.Fatal("invalid initial observation")
	}
	missing := observeCache(ctx, body("changed without telemetry", false), 150, &ModelUsage{InputTokens: 120, CacheReadKnown: &unknown})
	if missing.CachedTokens != nil || missing.CachedDrop {
		t.Fatal("missing telemetry became a measured cache miss")
	}
	next := observeCache(ctx, body("stable policy", true), 180, &ModelUsage{InputTokens: 120, CacheReadTokens: 70, CacheReadKnown: &known})
	if next.PrefixChange != "append-only" || !next.CachedDrop {
		t.Fatalf("lost last measured baseline: %+v", next)
	}
	data, _ := json.Marshal(next)
	if strings.Contains(string(data), "private fixture content") || strings.Contains(string(data), "stable policy") {
		t.Fatal("cache observation retained prompt text")
	}
	changed := fingerprintPrompt(body("stable policy", true))
	changed.items[0] = [32]byte{1}
	reference := fingerprintPrompt(body("stable policy", false))
	if reason, index := compareCachePrefixes(&reference, &changed); reason != "history" || index == nil || *index != 0 {
		t.Fatal("first changed input was not identified")
	}
	many := body("stable policy", false)
	many["input"] = make([]any, cacheFingerprintItems+1)
	bounded := fingerprintPrompt(many)
	if !bounded.bounded || len(bounded.items) != cacheFingerprintItems {
		t.Fatal("unbounded diagnostic history")
	}
	if reason, _ := compareCachePrefixes(&bounded, &bounded); reason != "unchecked-tail" {
		t.Fatal("bounded evidence claimed complete prefix equality")
	}
}
