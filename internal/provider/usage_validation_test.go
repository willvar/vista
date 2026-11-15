package provider

import "testing"

func TestValidModelUsage(t *testing.T) {
	for _, test := range []struct {
		name  string
		usage ModelUsage
		valid bool
	}{
		{"unknown cache", ModelUsage{InputTokens: 100, OutputTokens: 5}, true},
		{"reported zero", ModelUsage{InputTokens: 100, CacheWriteTokens: testInt64(0)}, true},
		{"cached input boundary", ModelUsage{InputTokens: 100, CacheReadTokens: 80, CacheWriteTokens: testInt64(20)}, true},
		{"maximum", ModelUsage{InputTokens: 1_000_000_000, OutputTokens: 1_000_000_000, CacheWriteTokens: testInt64(1_000_000_000), ReasoningTokens: 1_000_000_000}, true},
		{"negative input", ModelUsage{InputTokens: -1}, false},
		{"excess input", ModelUsage{InputTokens: 1_000_000_001}, false},
		{"negative output", ModelUsage{OutputTokens: -1}, false},
		{"excess output", ModelUsage{OutputTokens: 1_000_000_001}, false},
		{"negative read", ModelUsage{CacheReadTokens: -1}, false},
		{"read exceeds input", ModelUsage{InputTokens: 100, CacheReadTokens: 101}, false},
		{"negative write", ModelUsage{InputTokens: 100, CacheWriteTokens: testInt64(-1)}, false},
		{"excess write", ModelUsage{InputTokens: 1_000_000_000, CacheWriteTokens: testInt64(1_000_000_001)}, false},
		{"cache sum exceeds input", ModelUsage{InputTokens: 100, CacheReadTokens: 80, CacheWriteTokens: testInt64(30)}, false},
		{"negative reasoning", ModelUsage{ReasoningTokens: -1}, false},
		{"excess reasoning", ModelUsage{ReasoningTokens: 1_000_000_001}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ValidModelUsage(test.usage); got != test.valid {
				t.Fatalf("usage=%+v valid=%v want=%v", test.usage, got, test.valid)
			}
		})
	}
}
