package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"

	modelcfg "vista/internal/model"
	"vista/internal/modeltrace"
)

type cacheAttemptKey struct{}
type cacheAttempt struct {
	model   modelcfg.Model
	request *cacheRequestCapture
}
type cacheRequestCapture struct {
	mu          sync.Mutex
	data        []byte
	bytes       int
	owner       string
	compiled    bool
	fingerprint *cacheFingerprint
}
type cacheRequestBody struct {
	io.ReadCloser
	capture *cacheRequestCapture
}

func (b *cacheRequestBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.capture.mu.Lock()
	b.capture.bytes += n
	if !b.capture.compiled {
		remaining := cacheFingerprintBytes - len(b.capture.data)
		b.capture.data = append(b.capture.data, p[:min(n, remaining)]...)
	}
	b.capture.mu.Unlock()
	return n, err
}

func captureCacheRequest(request *http.Request, model modelcfg.Model) {
	attempt, _ := request.Context().Value(cacheAttemptKey{}).(*cacheAttempt)
	if attempt == nil || request.Body == nil {
		return
	}
	capture := &cacheRequestCapture{owner: requestOwner(request.Context(), model, request)}
	attempt.request = capture
	request.Body = &cacheRequestBody{ReadCloser: request.Body, capture: capture}
}

// Compile and release captured bytes as soon as HTTP finishes sending the body;
// a long generation retains only bounded hashes and counters.
func (b *cacheRequestBody) Close() error {
	err := b.ReadCloser.Close()
	b.capture.compile()
	return err
}
func (r *cacheRequestCapture) compile() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.compiled {
		return
	}
	r.compiled = true
	defer func() { r.data = nil }()
	decoder := json.NewDecoder(bytes.NewReader(r.data))
	decoder.UseNumber()
	var body map[string]any
	if r.bytes <= len(r.data) && decoder.Decode(&body) == nil {
		fingerprint := fingerprintPrompt(body)
		r.fingerprint = &fingerprint
	}
}
func (a *cacheAttempt) observe(ctx context.Context, usage *ModelUsage) {
	if a.request == nil {
		return
	}
	r := a.request
	r.compile()
	r.mu.Lock()
	fingerprint, count, owner := r.fingerprint, r.bytes, r.owner
	r.mu.Unlock()
	if fingerprint == nil {
		modeltrace.SetCacheObservation(ctx, modeltrace.CacheObservation{PrefixChange: "unchecked-request", RequestBytes: count, ComparisonBounded: true})
		return
	}
	observeCacheFingerprint(ctx, *fingerprint, count, usage, owner)
}
