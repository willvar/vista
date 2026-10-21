package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCatalogMirrorsTryEachAddressOnceAndStopAtSuccess(t *testing.T) {
	urls := []string{"first", "second", "third"}
	seen := map[string]bool{}
	got, err := fetchCatalogMirrors(context.Background(), "fixture", urls, func(ctx context.Context, url string) (string, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("mirror attempt has no deadline")
		}
		if seen[url] {
			t.Fatalf("mirror was retried within the same update: %s", url)
		}
		seen[url] = true
		if len(seen) < len(urls) {
			return "", context.DeadlineExceeded
		}
		return "complete catalog", nil
	})
	if err != nil || got != "complete catalog" || len(seen) != len(urls) {
		t.Fatalf("fallback failed: result=%q seen=%v err=%v", got, seen, err)
	}
	calls := 0
	_, err = fetchCatalogMirrors(context.Background(), "fixture", urls, func(context.Context, string) (string, error) {
		calls++
		return "catalog", nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("successful download continued to other mirrors: calls=%d err=%v", calls, err)
	}
}

func TestCatalogMirrorsReportAllFailuresAndRespectCancellation(t *testing.T) {
	urls := []string{"first", "second", "third"}
	_, err := fetchCatalogMirrors(context.Background(), "fixture", urls, func(_ context.Context, url string) (string, error) {
		return "", errors.New("invalid metadata from " + url)
	})
	if err == nil || !strings.Contains(err.Error(), "all fixture catalog sources failed") {
		t.Fatalf("missing aggregate failure: %v", err)
	}
	for _, url := range urls {
		if !strings.Contains(err.Error(), url) {
			t.Fatalf("failure omitted %s: %v", url, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err = fetchCatalogMirrors(ctx, "fixture", urls, func(context.Context, string) (string, error) {
		calls++
		cancel()
		return "late catalog", nil
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancelled update retried or returned a late result: calls=%d err=%v", calls, err)
	}
}
