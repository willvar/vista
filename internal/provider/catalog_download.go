package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"time"
)

const catalogDownloadTimeout = 15 * time.Second

// Try each address at most once in a fresh random order. Bound individual
// attempts so an unresponsive mirror leaves time for the remaining addresses.
func fetchCatalogMirrors[T any](ctx context.Context, source string, urls []string, fetch func(context.Context, string) (T, error)) (T, error) {
	var zero T
	var failures []error
	for _, index := range rand.Perm(len(urls)) {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		attempt, cancel := context.WithTimeout(ctx, catalogDownloadTimeout)
		result, err := fetch(attempt, urls[index])
		if err == nil {
			err = attempt.Err()
		}
		cancel()
		if err == nil {
			return result, nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", urls[index], err))
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	return zero, fmt.Errorf("all %s catalog sources failed: %w", source, errors.Join(failures...))
}

func fetchCatalogFile(ctx context.Context, client *http.Client, url, source string, limit int64) ([]byte, error) {
	data, _, _, err := fetchCatalogResponse(ctx, client, url, source, limit, "")
	return data, err
}

func fetchCatalogResponse(ctx context.Context, client *http.Client, url, source string, limit int64, etag string) ([]byte, string, bool, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Vista")
	req.Header.Set("Cache-Control", "no-cache")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, "", false, fmt.Errorf("fetch %s catalog: %w", source, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified && etag != "" {
		return nil, etag, true, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, "", false, fmt.Errorf("%s catalog returned HTTP %d", source, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, "", false, fmt.Errorf("read %s catalog: %w", source, err)
	}
	if int64(len(data)) > limit {
		return nil, "", false, fmt.Errorf("%s catalog response is too large", source)
	}
	return data, response.Header.Get("ETag"), false, nil
}

// Hash the normalized metadata: mirror-specific scores, pricing, key ordering
// and JSON formatting must not cause an otherwise unchanged catalog to update.
func catalogRevision(providers map[string]map[string]OMPModel) (string, error) {
	data, err := json.Marshal(providers)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}
