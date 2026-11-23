package generation

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"vista/internal/model"
	"vista/internal/network"
	"vista/internal/provider"
	"vista/internal/resources"
	"vista/internal/useragent"
)

const maxResponseJSON = 72 << 20 // base64 of one original image plus metadata

var ErrUpstreamCanceled = errors.New("upstream confirmed cancellation")
var mediaClient = network.NewClient(http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, network.ConnectionScope)

type Asset struct{ Body io.ReadCloser }
type wire map[string]any

type request struct {
	ctx    context.Context
	base   *url.URL
	target model.GenerationTarget
	model  string
	key    string
	wait   time.Duration
}

func newRequest(ctx context.Context, target model.GenerationTarget) (request, error) {
	if target.Error != "" {
		return request{}, errors.New(target.Error)
	}
	m := target.Model
	spec, ok := Find(m.ProviderID, target.Kind)
	if !ok || !ValidateModelID(m.ID) {
		return request{}, errors.New("no compatible generation adapter is configured")
	}
	base := m.BaseURL
	if target.Kind == model.ImageOutput && m.ProviderID == "microsoft-foundry" {
		base = connectionMediaBase(spec, m)
	}
	if target.Connection != nil {
		base = connectionMediaBase(spec, *target.Connection)
		m.APIKey = target.Connection.APIKey
		m.Network = target.Connection.Network
		m.ProxyID = target.Connection.ProxyID
		m.Headers = target.Connection.Headers
		m.Custom, m.Authentication = target.Connection.Custom, target.Connection.Authentication
		m.OpenAIOrganization, m.OpenAIProject = target.Connection.OpenAIOrganization, target.Connection.OpenAIProject
	}
	if base == "" && !m.Custom {
		base = spec.Base
	}
	if !m.Custom && !compatibleModel(m.ProviderID, target.Kind, m.ID) {
		return request{}, errors.New("the selected model does not use this generation interface")
	}
	parsed, err := Endpoint(base)
	if err != nil {
		return request{}, err
	}
	if m.ProviderID == "onnx" && (!IsLoopbackService(parsed) || m.ProxyID != "" || m.APIKey != "") {
		return request{}, errors.New("the local ONNX scorer requires a direct loopback connection without credentials")
	}
	ctx, m, err = m.BindNetwork(ctx)
	if err != nil {
		return request{}, err
	}
	if m.APIKey == "" && !m.Custom && m.ProviderID != "comfy" && m.ProviderID != "litellm" && m.ProviderID != "onnx" && !(m.ProviderID == "typesafe" && IsLoopbackService(parsed)) {
		return request{}, errors.New("this generation connection requires credentials")
	}
	target.Model = m
	return request{ctx: ctx, base: parsed, target: target, model: m.ID, key: m.APIKey, wait: 2 * time.Second}, nil
}

func connectionMediaBase(spec Spec, source model.Model) string {
	if source.Custom {
		return provider.CustomBaseURL(source.BaseURL, source.Provider)
	}
	if source.BaseURL == "" {
		return spec.Base
	}
	base := strings.TrimRight(source.BaseURL, "/")
	switch spec.ID {
	case "opencode":
		// Console hosts System One separately from its conversation routes.
		// Do not rewrite a saved alternative host or a Go subscription.
		if u, err := Endpoint(base); err == nil && u.Scheme == "https" && strings.EqualFold(u.Hostname(), "opencode.ai") && (u.Port() == "" || u.Port() == "443") {
			return spec.Base
		}
	case "clawrouter":
		if !strings.HasSuffix(base, "/v1") {
			return base + "/v1"
		}
	case "google":
		if strings.HasSuffix(base, "/v1beta") {
			return base
		}
		return base + "/v1beta"
	case "minimax", "minimax-portal":
		return strings.TrimSuffix(strings.TrimSuffix(base, "/v1"), "/anthropic")
	case "alibaba", "qwen":
		return strings.TrimSuffix(strings.TrimSuffix(base, "/compatible-mode/v1"), "/api/v1")
	case "together":
		return strings.TrimSuffix(strings.TrimSuffix(base, "/v1"), "/v2")
	case "runway":
		return strings.TrimSuffix(base, "/v1")
	case "novita":
		return strings.TrimSuffix(strings.TrimSuffix(base, "/v1"), "/openai")
	case "microsoft-foundry":
		if strings.HasSuffix(base, "/openai") || strings.HasSuffix(base, "/mai") {
			return base + "/v1"
		}
		if u, err := Endpoint(base); err == nil && u.Path == "" {
			if source.ProviderID == "azure" || strings.HasSuffix(strings.ToLower(u.Hostname()), ".openai.azure.com") {
				return base + "/openai/v1"
			}
			return base + "/mai/v1"
		}
	}
	return base
}

func getValue(v any, path ...any) any {
	for _, k := range path {
		switch k := k.(type) {
		case string:
			switch o := v.(type) {
			case wire:
				v = o[k]
			case map[string]any:
				v = o[k]
			default:
				return nil
			}
		case int:
			a, ok := v.([]any)
			if !ok || k < 0 || k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}
func text(v any, path ...any) string { v = getValue(v, path...); s, _ := v.(string); return s }
func first(values ...string) string {
	for _, s := range values {
		if s != "" {
			return s
		}
	}
	return ""
}
func jobID(v any, path ...any) string {
	x := getValue(v, path...)
	switch x := x.(type) {
	case string:
		return x
	case json.Number:
		return string(x)
	}
	return ""
}

func (c request) route(path string) string {
	return strings.TrimRight(c.base.String(), "/") + "/" + strings.TrimLeft(path, "/")
}
func sameOrigin(a, b *url.URL) bool { return a.Scheme == b.Scheme && strings.EqualFold(a.Host, b.Host) }

func (c request) api(method, raw string, body any, extra http.Header) (wire, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	resp, err := c.send(method, raw, reader, extra, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// A paid submission is never retried, even if its reply is missing or bad.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("generation service returned HTTP %d", resp.StatusCode)
	}
	limit := int64(2 << 20)
	if c.target.Kind == model.ImageOutput {
		limit = maxResponseJSON
	} else if c.target.Kind == model.VideoOutput && (c.target.Model.ProviderID == "deepinfra" || c.target.Model.ProviderID == "google") {
		// These APIs can embed the original video rather than a download URL.
		limit = int64(base64.StdEncoding.EncodedLen(resources.MaxVideoBytes)) + (1 << 20)
	}
	r := &io.LimitedReader{R: resp.Body, N: limit + 1}
	d := json.NewDecoder(r)
	d.UseNumber()
	var result wire
	if d.Decode(&result) != nil || result == nil || d.Decode(new(any)) != io.EOF || r.N <= 0 {
		return nil, errors.New("generation service returned invalid or oversized JSON")
	}
	if err := c.resultError(result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c request) resultError(result wire) error {
	if result["error"] != nil && result["error"] != "" {
		return errors.New("generation service returned an error")
	}
	// Some APIs report business failures in a successful HTTP response.
	var code any
	want := 0
	switch c.target.Model.ProviderID {
	case "kie":
		code, want = result["code"], 200
	case "pixverse":
		code = result["ErrCode"]
	case "minimax", "minimax-portal":
		code = getValue(result, "base_resp", "status_code")
	case "alibaba", "qwen":
		if text(result, "code") != "" {
			return errors.New("DashScope rejected the generation request")
		}
	}
	if code != nil {
		actual, ok := numeric(code)
		if !ok || actual != want {
			return errors.New("generation service reported a non-success API code")
		}
	}
	return nil
}

func (c request) send(method, raw string, body io.Reader, extra http.Header, auth bool) (*http.Response, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("invalid generation service URL")
	}
	same := sameOrigin(c.base, u)
	if auth && !same {
		return nil, errors.New("generation API returned a task URL outside its connection")
	}
	ctx := c.ctx
	if !same {
		if u.Scheme != "https" {
			return nil, errors.New("external generation result must use HTTPS")
		}
		res, e := network.LookupURL(ctx, u)
		if e != nil || len(res.Addresses) == 0 {
			return nil, errors.New("could not resolve generation result host")
		}
		for _, ip := range res.Addresses {
			a, ok := netip.AddrFromSlice(ip)
			a = a.Unmap()
			if !ok || !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || netip.MustParsePrefix("100.64.0.0/10").Contains(a) {
				return nil, errors.New("generation result resolves to a non-public address")
			}
		}
		ctx, e = network.WithAddresses(ctx, u.Hostname(), res.Addresses)
		if e != nil {
			return nil, e
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, errors.New("invalid generation service request")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		if c.target.Model.Custom {
			provider.ApplyCustomRequestHeaders(req.Header, c.target.Model.Authentication, c.key, c.target.Model.Headers)
		} else {
			for k, v := range c.target.Model.Headers {
				req.Header.Set(k, v)
			}
			if c.target.Model.OpenAIOrganization != "" {
				req.Header.Set("OpenAI-Organization", c.target.Model.OpenAIOrganization)
			}
			if c.target.Model.OpenAIProject != "" {
				req.Header.Set("OpenAI-Project", c.target.Model.OpenAIProject)
			}
			if c.key != "" {
				switch c.target.Model.ProviderID {
				case "google":
					req.Header.Set("x-goog-api-key", c.key)
				case "fal":
					req.Header.Set("Authorization", "Key "+c.key)
				case "pixverse":
					req.Header.Set("API-KEY", c.key)
				case "microsoft-foundry":
					req.Header.Set("api-key", c.key)
				case "comfy":
					req.Header.Set("X-API-Key", c.key)
				default:
					req.Header.Set("Authorization", "Bearer "+c.key)
				}
			}
		}
		for k, v := range extra {
			req.Header[http.CanonicalHeaderKey(k)] = v
		}
		if c.target.Model.ProviderID == "pixverse" {
			req.Header.Set("Ai-Trace-Id", uuid.NewString())
		}
	}
	// Disable native replay of a billed POST, even on a reused idle connection.
	if method == http.MethodPost {
		req.GetBody = nil
		req.Header.Del("Idempotency-Key")
		req.Header.Del("X-Idempotency-Key")
	}
	resp, err := useragent.Do(mediaClient, req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("generation request failed; submission may have been accepted")
	}
	return resp, nil
}

func (c request) asset(raw, encoded string) (Asset, error) {
	if strings.HasPrefix(raw, "data:") {
		prefix, b, ok := strings.Cut(raw, ",")
		if !ok || !strings.HasSuffix(prefix, ";base64") {
			return Asset{}, errors.New("unsupported inline generation result")
		}
		encoded = b
		raw = ""
	}
	if encoded != "" {
		return Asset{Body: io.NopCloser(base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded)))}, nil
	}
	if raw == "" {
		return Asset{}, errors.New("generation service did not return a media result")
	}
	current, err := c.base.Parse(raw)
	if err != nil {
		return Asset{}, errors.New("invalid generation result URL")
	}
	// Credentials apply only to the exact configured API origin. Revalidate
	// every redirect, and bind resolved IPs for external signed/CDN URLs.
	for range 4 {
		resp, e := c.send(http.MethodGet, current.String(), nil, nil, sameOrigin(c.base, current))
		if e != nil {
			return Asset{}, e
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			next, e := resp.Location()
			resp.Body.Close()
			if e != nil {
				return Asset{}, errors.New("invalid media redirect")
			}
			current = next
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return Asset{}, fmt.Errorf("media download returned HTTP %d", resp.StatusCode)
		}
		return Asset{Body: resp.Body}, nil
	}
	return Asset{}, errors.New("too many generation result redirects")
}

func (c request) poll(path string, terminal func(wire) (bool, error)) (wire, error) {
	return c.pollWithHeaders(path, nil, terminal)
}
func (c request) pollWithHeaders(path string, headers http.Header, terminal func(wire) (bool, error)) (wire, error) {
	for {
		select {
		case <-c.ctx.Done():
			return nil, c.ctx.Err()
		case <-time.After(c.wait):
		}
		value, err := c.api(http.MethodGet, path, nil, headers)
		if err != nil {
			return nil, err
		}
		done, err := terminal(value)
		if err != nil || done {
			return value, err
		}
	}
}

func status(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "done", "succeeded", "success", "completed", "complete", "finished", "ready", "task_status_succeed":
		return true, nil
	case "canceled", "cancelled":
		return true, ErrUpstreamCanceled
	case "failed", "fail", "failure", "error", "expired", "task_status_fail", "task_status_failed":
		return true, errors.New("upstream generation failed")
	case "pending", "queued", "in_queue", "in_progress", "processing", "running", "submitted", "starting", "preparing", "task_status_processing", "task_status_queueing":
		return false, nil
	default:
		return false, errors.New("upstream returned an unrecognized generation state")
	}
}
func validTaskID(id string) bool {
	if len(id) < 1 || len(id) > 256 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return id != "." && id != ".."
}
func numeric(v any) (int, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, e := strconv.Atoi(string(n))
	return i, e == nil
}
