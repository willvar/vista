// Package modeltrace records bounded, credential-redacted model HTTP diagnostics.
// Capture is opt-in per execution and does not depend on browser event delivery.
package modeltrace

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"vista/internal/appdirs"
)

const maxRequestBytes = 1 << 20
const maxResponseBytes = 2 << 20
const maxExchanges = 8
const maxChunks = 2048
const maxHeaderBytes = 32 << 10

type Scope struct {
	Enabled   bool   `json:"-"`
	SessionID string `json:"sessionId"`
	RunID     string `json:"runId"`
	TaskID    string `json:"taskId,omitempty"`
	Turn      int32  `json:"turn,omitempty"`
	Purpose   string `json:"purpose,omitempty"`
}

type Parsing struct {
	Line                  int      `json:"line,omitempty"`
	Event                 int      `json:"event,omitempty"`
	ReasoningFields       []string `json:"reasoningFields,omitempty"`
	ReasoningFragments    int      `json:"reasoningFragments"`
	ReasoningBlocks       int      `json:"reasoningBlocks"`
	ReasoningBytes        int      `json:"reasoningBytes"`
	VisibleReasoningBytes int      `json:"visibleReasoningBytes"`
}

type Record struct {
	Scope
	ID           string      `json:"id"`
	Provider     string      `json:"provider"`
	Protocol     string      `json:"protocol"`
	Model        string      `json:"model"`
	StartedAt    time.Time   `json:"startedAt"`
	DurationMS   float64     `json:"durationMs"`
	Error        string      `json:"error,omitempty"`
	FinishReason string      `json:"finishReason,omitempty"`
	Parsing      *Parsing    `json:"parsing,omitempty"`
	Exchanges    []*Exchange `json:"exchanges"`
	Truncated    bool        `json:"truncated"`
}

type Body struct {
	Content   string `json:"content"`
	Encoding  string `json:"encoding"`
	Bytes     int64  `json:"bytes"`
	Truncated bool   `json:"truncated"`
	data      []byte
}

type Chunk struct {
	Offset int64   `json:"offset"`
	Bytes  int     `json:"bytes"`
	AtMS   float64 `json:"atMs"`
}

type Exchange struct {
	Method           string      `json:"method"`
	URL              string      `json:"url"`
	RequestHeaders   http.Header `json:"requestHeaders"`
	Request          Body        `json:"request"`
	Status           int         `json:"status"`
	ResponseHeaders  http.Header `json:"responseHeaders,omitempty"`
	Response         Body        `json:"response"`
	StartedMS        float64     `json:"startedMs"`
	HeadersMS        float64     `json:"headersMs"`
	DurationMS       float64     `json:"durationMs"`
	Chunks           []Chunk     `json:"chunks"`
	TimingsTruncated bool        `json:"timingsTruncated"`
	EOF              bool        `json:"eof"`
	ReadError        string      `json:"readError,omitempty"`
	Error            string      `json:"error,omitempty"`
}

type scopeKey struct{}
type captureKey struct{}
type observerKey struct{}

func WithScope(ctx context.Context, scope Scope, saved ...func(Summary)) context.Context {
	ctx = context.WithValue(ctx, scopeKey{}, scope)
	if len(saved) > 0 {
		ctx = context.WithValue(ctx, observerKey{}, saved[0])
	}
	return ctx
}

type Capture struct {
	mu                sync.Mutex
	finished          bool
	record            Record
	dir               string
	secrets           []string
	requestRemaining  int
	responseRemaining int
	saved             func(Summary)
}

// Start excludes login and credential-refresh requests: only the context passed
// to the inference adapter receives the capture. Failure to record never fails inference.
func Start(ctx context.Context, provider, protocol, model string, secrets []string) (context.Context, *Capture) {
	scope, _ := ctx.Value(scopeKey{}).(Scope)
	if !scope.Enabled || !validID(scope.SessionID) || !validID(scope.RunID) {
		return ctx, nil
	}
	dir, err := appdirs.State()
	if err != nil {
		return ctx, nil
	}
	c := &Capture{dir: dir, secrets: append([]string(nil), secrets...), requestRemaining: maxRequestBytes, responseRemaining: maxResponseBytes,
		record: Record{Scope: scope, ID: uuid.NewString(), Provider: provider, Protocol: protocol, Model: model, StartedAt: time.Now().UTC(), Exchanges: []*Exchange{}}}
	c.saved, _ = ctx.Value(observerKey{}).(func(Summary))
	return context.WithValue(ctx, captureKey{}, c), c
}

func SetParsing(ctx context.Context, parsing Parsing) {
	if c, _ := ctx.Value(captureKey{}).(*Capture); c != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		if !c.finished {
			c.record.Parsing = &parsing
		}
	}
}

func (c *Capture) Finish(finishReason string, err error) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return
	}
	c.finished = true
	c.record.DurationMS = time.Since(c.record.StartedAt).Seconds() * 1000
	c.record.FinishReason = finishReason
	c.record.Model = c.redact(c.record.Model, false)
	c.record.FinishReason = c.redact(c.record.FinishReason, false)
	if err != nil {
		c.record.Error = c.clip(c.redact(err.Error(), false), 8<<10)
	}
	for _, exchange := range c.record.Exchanges {
		exchange.URL = c.redact(exchange.URL, false)
		exchange.Error = c.redact(exchange.Error, false)
		exchange.ReadError = c.redact(exchange.ReadError, false)
		c.cleanHeaders(exchange.RequestHeaders)
		c.cleanHeaders(exchange.ResponseHeaders)
		exchange.URL = c.clip(exchange.URL, 8<<10)
		exchange.Error = c.clip(exchange.Error, 8<<10)
		exchange.ReadError = c.clip(exchange.ReadError, 8<<10)
		c.cleanBody(&exchange.Request)
		c.cleanBody(&exchange.Response)
		c.record.Truncated = c.record.Truncated || exchange.Request.Truncated || exchange.Response.Truncated || exchange.TimingsTruncated
	}
	if err := Save(c.dir, c.record); err != nil {
		log.Printf("model diagnostics could not be saved: call=%s", c.record.ID)
	} else if c.saved != nil {
		c.saved(c.record.Summary())
	}
}

// Transport observes the final authenticated/signed request without reading
// ahead or changing stream completion, cancellation, or error handling.
type Transport struct{ Base http.RoundTripper }

func (t Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	c, _ := request.Context().Value(captureKey{}).(*Capture)
	if c == nil {
		return base.RoundTrip(request)
	}
	c.mu.Lock()
	if c.finished {
		c.mu.Unlock()
		return base.RoundTrip(request)
	}
	if len(c.record.Exchanges) >= maxExchanges {
		c.record.Truncated = true
		c.mu.Unlock()
		return base.RoundTrip(request)
	}
	c.collectHeaders(request.Header)
	endpoint := *request.URL
	if endpoint.User != nil {
		c.secrets = append(c.secrets, endpoint.User.Username())
		if password, ok := endpoint.User.Password(); ok {
			c.secrets = append(c.secrets, password)
		}
		endpoint.User = url.User("[redacted]")
	}
	query := endpoint.Query()
	queryRedacted := false
	for key, values := range query {
		if sensitiveHeader(key) {
			c.secrets = append(c.secrets, values...)
			query[key] = []string{"[redacted]"}
			queryRedacted = true
		}
	}
	if queryRedacted {
		endpoint.RawQuery = query.Encode()
	}
	e := &Exchange{Method: request.Method, URL: endpoint.String(), RequestHeaders: request.Header.Clone(), StartedMS: c.elapsed(), Chunks: []Chunk{}}
	c.record.Exchanges = append(c.record.Exchanges, e)
	c.mu.Unlock()
	// Observe headers emitted by net/http too, including Content-Length and
	// transport defaults that are absent from the adapter's Header map.
	wireTrace := &httptrace.ClientTrace{WroteHeaderField: func(key string, values []string) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.finished {
			return
		}
		key = http.CanonicalHeaderKey(key)
		e.RequestHeaders[key] = append([]string(nil), values...)
		c.collectHeaders(http.Header{key: values})
	}}
	copy := request.Clone(httptrace.WithClientTrace(request.Context(), wireTrace))
	if request.Body != nil {
		copy.Body = &bodyObserver{ReadCloser: request.Body, capture: c, exchange: e}
	}
	response, err := base.RoundTrip(copy)
	c.mu.Lock()
	e.HeadersMS = c.elapsed()
	if err != nil {
		e.Error = err.Error()
		e.DurationMS = c.elapsed() - e.StartedMS
	}
	if response != nil {
		e.Status, e.ResponseHeaders = response.StatusCode, response.Header.Clone()
		c.collectHeaders(response.Header)
		response.Body = &bodyObserver{ReadCloser: response.Body, capture: c, exchange: e, response: true}
	}
	c.mu.Unlock()
	return response, err
}

type bodyObserver struct {
	io.ReadCloser
	capture  *Capture
	exchange *Exchange
	response bool
}

func (b *bodyObserver) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	c, e := b.capture, b.exchange
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return n, err
	}
	body, remaining := &e.Request, &c.requestRemaining
	if b.response {
		body, remaining = &e.Response, &c.responseRemaining
		if n > 0 {
			if len(e.Chunks) < maxChunks {
				e.Chunks = append(e.Chunks, Chunk{Offset: body.Bytes, Bytes: n, AtMS: c.elapsed()})
			} else {
				e.TimingsTruncated = true
			}
		}
		if err == io.EOF {
			e.EOF = true
		} else if err != nil {
			e.ReadError = err.Error()
		}
	}
	body.Bytes += int64(n)
	keep := min(n, *remaining)
	body.data = append(body.data, p[:keep]...)
	*remaining -= keep
	if keep < n {
		body.Truncated = true
	}
	return n, err
}

func (b *bodyObserver) Close() error {
	err := b.ReadCloser.Close()
	if b.response {
		b.capture.mu.Lock()
		if !b.capture.finished {
			b.exchange.DurationMS = b.capture.elapsed() - b.exchange.StartedMS
		}
		b.capture.mu.Unlock()
	}
	return err
}

func (c *Capture) elapsed() float64 { return time.Since(c.record.StartedAt).Seconds() * 1000 }

func sensitiveHeader(name string) bool {
	name = strings.ToLower(name)
	return strings.Contains(name, "auth") || strings.Contains(name, "key") || strings.Contains(name, "token") || strings.Contains(name, "secret") || strings.Contains(name, "password") || strings.Contains(name, "signature") || strings.Contains(name, "credential") || strings.Contains(name, "cookie")
}

func (c *Capture) collectHeaders(headers http.Header) {
	for key, values := range headers {
		if sensitiveHeader(key) {
			for _, value := range values {
				c.secrets = append(c.secrets, value)
				if parts := strings.Fields(value); len(parts) == 2 {
					c.secrets = append(c.secrets, parts[1])
				}
				if strings.EqualFold(key, "Authorization") {
					for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == ' ' || r == ',' }) {
						name, token, found := strings.Cut(part, "=")
						if found && sensitiveHeader(name) {
							c.secrets = append(c.secrets, token)
						}
					}
				}
			}
		}
	}
	for _, cookie := range (&http.Request{Header: headers}).Cookies() {
		c.secrets = append(c.secrets, cookie.Value)
	}
	for _, cookie := range (&http.Response{Header: headers}).Cookies() {
		c.secrets = append(c.secrets, cookie.Value)
	}
}

func (c *Capture) cleanHeaders(headers http.Header) {
	remaining := maxHeaderBytes
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for index, key := range keys {
		if index >= 256 {
			delete(headers, key)
			c.record.Truncated = true
			continue
		}
		values := headers[key]
		if sensitiveHeader(key) {
			headers[key] = []string{"[redacted]"}
		} else {
			for i, value := range values {
				values[i] = c.redact(value, false)
			}
		}
		if len(key) > remaining {
			delete(headers, key)
			c.record.Truncated = true
			continue
		}
		remaining -= len(key)
		var kept []string
		for i, value := range headers[key] {
			if i >= 64 {
				c.record.Truncated = true
				break
			}
			kept = append(kept, c.clip(value, remaining))
			remaining -= len(kept[len(kept)-1])
		}
		headers[key] = kept
	}
}

func (c *Capture) clip(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	c.record.Truncated = true
	end := limit
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}

func (c *Capture) redact(value string, truncated bool) string {
	var secrets []string
	for _, secret := range c.secrets {
		if secret == "" {
			continue
		}
		escaped, _ := json.Marshal(secret)
		secrets = append(secrets, secret, url.QueryEscape(secret), url.PathEscape(secret), string(escaped[1:len(escaped)-1]))
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		value = strings.ReplaceAll(value, secret, "[redacted]")
	}
	if truncated {
		for _, secret := range secrets {
			// A retained prefix must not reveal part of a credential split at
			// the capture limit. Redact before turning binary data into base64.
			for n := min(len(secret)-1, len(value)); n > 0; n-- {
				if strings.HasSuffix(value, secret[:n]) {
					value = value[:len(value)-n] + "[redacted]"
					break
				}
			}
		}
	}
	return value
}

func (c *Capture) cleanBody(body *Body) {
	value := c.redact(string(body.data), body.Truncated)
	body.Encoding = "utf-8"
	if !utf8.ValidString(value) {
		value = base64.StdEncoding.EncodeToString([]byte(value))
		body.Encoding = "base64"
	}
	body.Content, body.data = value, nil
}
