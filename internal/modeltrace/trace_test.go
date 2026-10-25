package modeltrace

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixtureCapture(t *testing.T, secrets ...string) (context.Context, *Capture) {
	t.Helper()
	c := &Capture{dir: t.TempDir(), secrets: secrets, requestRemaining: maxRequestBytes, responseRemaining: maxResponseBytes,
		record: Record{Scope: Scope{Enabled: true, SessionID: uuid.NewString(), RunID: uuid.NewString(), TaskID: "main/child", Turn: 3, Purpose: "conversation"}, ID: uuid.NewString(), Provider: "fixture", Protocol: "openai-chat", Model: "fixture", StartedAt: time.Now().UTC(), Exchanges: []*Exchange{}}}
	return context.WithValue(context.Background(), captureKey{}, c), c
}

func savedRecord(t *testing.T, c *Capture) Record {
	t.Helper()
	list, err := List(c.dir, c.record.SessionID, c.record.RunID)
	if err != nil || len(list) != 1 {
		t.Fatalf("trace index: %+v %v", list, err)
	}
	record, err := Read(c.dir, c.record.SessionID, c.record.RunID, list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestSavedObserverRunsOnceAfterReadableFileAndNeverOnSaveFailure(t *testing.T) {
	_, c := fixtureCapture(t)
	calls := 0
	c.saved = func(summary Summary) {
		calls++
		saved, err := ReadSummary(c.dir, summary.SessionID, summary.RunID, summary.ID)
		if err != nil || saved.ID != summary.ID {
			t.Fatalf("observer preceded saved file: %+v %v", saved, err)
		}
	}
	c.Finish("stop", nil)
	c.Finish("stop", nil)
	if calls != 1 {
		t.Fatalf("saved observer called %d times", calls)
	}
	_, failed := fixtureCapture(t)
	failed.dir = filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(failed.dir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	failed.saved = func(Summary) { t.Fatal("failed capture published a record") }
	failed.Finish("stop", nil)
}

func TestTransportCapturesWireWithoutChangingItAndRedactsSplitSecrets(t *testing.T) {
	secret := "fixture-secret-with-spaces and\"quotes"
	ctx, c := fixtureCapture(t, secret)
	responseText := "data: " + secret + "\n\ndata: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer "+secret || string(body) != secret {
			t.Error("capture changed upstream credentials or body")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Set-Cookie", "auth=fixture-cookie; HttpOnly")
		io.WriteString(w, responseText+"fixture-cookie")
	}))
	defer srv.Close()
	httpClient := &http.Client{Transport: Transport{}}
	for range 2 {
		r, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"?key=fixture-query-secret&model=fixture", strings.NewReader(secret))
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Set("User-Agent", "Fixture/1")
		response, err := httpClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		// Force a credential to span several application Reads.
		var received strings.Builder
		buffer := make([]byte, 5)
		for {
			n, err := response.Body.Read(buffer)
			received.Write(buffer[:n])
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		response.Body.Close()
		if received.String() != responseText+"fixture-cookie" {
			t.Fatal("capture changed response bytes")
		}
	}
	SetParsing(ctx, Parsing{Event: 2, ReasoningFragments: 1000, ReasoningBlocks: 1})
	c.Finish("stop", nil)
	record := savedRecord(t, c)
	encoded, _ := json.Marshal(record)
	for _, value := range []string{secret, "fixture-query-secret", "fixture-cookie"} {
		if strings.Contains(string(encoded), value) {
			t.Fatalf("credential leaked: %q", value)
		}
	}
	if len(record.Exchanges) != 2 || record.TaskID != "main/child" || record.Turn != 3 || record.Parsing.ReasoningFragments != 1000 {
		t.Fatalf("request association: %+v", record)
	}
	for _, exchange := range record.Exchanges {
		if exchange.Status != 200 || exchange.RequestHeaders.Get("Authorization") != "[redacted]" || exchange.RequestHeaders.Get("User-Agent") != "Fixture/1" || exchange.RequestHeaders.Get("Content-Length") == "" || !exchange.EOF || exchange.Response.Encoding != "utf-8" || !strings.Contains(exchange.Response.Content, "data: [DONE]\n\n") {
			t.Fatalf("exchange metadata: %+v", exchange)
		}
		if len(exchange.Chunks) < 2 || exchange.Chunks[0].Offset != 0 || exchange.Chunks[1].Offset != 5 || exchange.HeadersMS < exchange.StartedMS || exchange.DurationMS <= 0 {
			t.Fatalf("receive timings: %+v", exchange)
		}
	}
}

func TestCaptureDisabledDoesNotCreateRecorder(t *testing.T) {
	ctx := WithScope(context.Background(), Scope{SessionID: uuid.NewString(), RunID: uuid.NewString()})
	_, capture := Start(ctx, "p", "openai-chat", "m", nil)
	if capture != nil {
		t.Fatal("debug-off created capture")
	}
	SetParsing(ctx, Parsing{Event: 1})
	capture.Finish("", errors.New("uncaptured"))
}

type interruptedBody struct{ sent bool }

func (b *interruptedBody) Read(p []byte) (int, error) {
	if b.sent {
		return 0, io.ErrUnexpectedEOF
	}
	b.sent = true
	return copy(p, "data: partial\n"), nil
}
func (b *interruptedBody) Close() error { return nil }

func TestCapturePreservesPartialResponseAndTransportFailures(t *testing.T) {
	ctx, c := fixtureCapture(t, "fixture-token")
	transport := Transport{Base: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/fail" {
			return nil, errors.New("network failed fixture-token")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &interruptedBody{}, Request: r}, nil
	})}
	request, _ := http.NewRequestWithContext(ctx, "POST", "https://example.test/partial", nil)
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != io.ErrUnexpectedEOF || string(data) != "data: partial\n" {
		t.Fatalf("partial stream changed: %q %v", data, err)
	}
	request, _ = http.NewRequestWithContext(ctx, "POST", "https://example.test/fail", nil)
	if _, err := transport.RoundTrip(request); err == nil {
		t.Fatal("transport error hidden")
	}
	c.Finish("", errors.New("parse failed fixture-token"))
	record := savedRecord(t, c)
	if record.Exchanges[0].EOF || record.Exchanges[0].ReadError != "unexpected EOF" || record.Exchanges[0].Response.Content != "data: partial\n" || record.Error != "parse failed [redacted]" || record.Exchanges[1].Error != "network failed [redacted]" {
		t.Fatalf("lost failed request: %+v", record)
	}
}

func TestCaptureLimitsRetainByteCountsAndRedactTruncatedCredential(t *testing.T) {
	secret := "credential-crossing-the-limit"
	ctx, c := fixtureCapture(t, secret)
	requestText := strings.Repeat("x", maxRequestBytes+100)
	responseText := strings.Repeat("r", maxResponseBytes-10) + secret + " trailing data"
	transport := Transport{Base: transportFunc(func(r *http.Request) (*http.Response, error) {
		count, err := io.Copy(io.Discard, r.Body)
		if err != nil || count != int64(len(requestText)) {
			t.Errorf("request was truncated on the wire: %d %v", count, err)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(responseText)), Request: r}, nil
	})}
	r, _ := http.NewRequestWithContext(ctx, "POST", "https://example.test", strings.NewReader(requestText))
	response, err := transport.RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	count, err := io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if err != nil || count != int64(len(responseText)) {
		t.Fatalf("response was truncated on the wire: %d %v", count, err)
	}
	c.Finish("stop", nil)
	record := savedRecord(t, c)
	e := record.Exchanges[0]
	if !record.Truncated || !e.Request.Truncated || !e.Response.Truncated || e.Response.Bytes != int64(len(responseText)) || !strings.HasSuffix(e.Response.Content, "[redacted]") || len(e.Request.Content) != maxRequestBytes {
		t.Fatal("capture bounds or truncated redaction failed")
	}
}

func TestTruncatedCaptureRedactsCompleteAndPartialSecrets(t *testing.T) {
	_, c := fixtureCapture(t, "abc-a")
	for _, sample := range []struct{ input, want string }{
		{"prefix abc-a", "prefix [redacted]"},
		{"prefix abc", "prefix [redacted]"},
		{"prefix abc-a tail", "prefix [redacted] tail"},
	} {
		if got := c.redact(sample.input, true); got != sample.want {
			t.Fatalf("redaction %q: got %q want %q", sample.input, got, sample.want)
		}
	}
}

func TestCaptureBinaryResponseIsExplicitlyEncoded(t *testing.T) {
	ctx, c := fixtureCapture(t, "binary-secret")
	transport := Transport{Base: transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("\xff\x00binary-secret\x01")), Request: r}, nil
	})}
	r, _ := http.NewRequestWithContext(ctx, "POST", "https://example.test", nil)
	response, _ := transport.RoundTrip(r)
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	c.Finish("stop", nil)
	record := savedRecord(t, c)
	body := record.Exchanges[0].Response
	data, err := base64.StdEncoding.DecodeString(body.Content)
	if body.Encoding != "base64" || err != nil || string(data) != "\xff\x00[redacted]\x01" {
		t.Fatalf("binary response: %s %q %v", body.Encoding, data, err)
	}
}

func TestCaptureExchangeAndTimingLimitsDoNotStopHTTP(t *testing.T) {
	ctx, c := fixtureCapture(t)
	calls := 0
	transport := Transport{Base: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{"X-Large": []string{strings.Repeat("h", maxHeaderBytes+1)}}, Body: io.NopCloser(strings.NewReader(strings.Repeat("r", maxChunks+1))), Request: r}, nil
	})}
	for range maxExchanges + 2 {
		r, _ := http.NewRequestWithContext(ctx, "POST", "https://example.test", nil)
		response, err := transport.RoundTrip(r)
		if err != nil {
			t.Fatal(err)
		}
		buffer := make([]byte, 1)
		for {
			_, err := response.Body.Read(buffer)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		response.Body.Close()
	}
	c.Finish("stop", nil)
	record := savedRecord(t, c)
	if calls != maxExchanges+2 || len(record.Exchanges) != maxExchanges || !record.Truncated {
		t.Fatal("exchange recording limit affected actual HTTP")
	}
	for _, e := range record.Exchanges {
		if len(e.Chunks) != maxChunks || !e.TimingsTruncated || len(e.ResponseHeaders.Get("X-Large")) > maxHeaderBytes || len(e.Response.Content) != maxChunks+1 {
			t.Fatal("timing/header cap affected response body")
		}
	}
}

func TestStoreEnforcesTotalByteBudget(t *testing.T) {
	dir := t.TempDir()
	session, run := uuid.NewString(), uuid.NewString()
	var names []string
	for i := range 2 {
		path, _ := recordPath(session, run, uuid.NewString())
		path = filepath.Join(dir, directory, path)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maxStorageBytes/2 + 1); err != nil {
			t.Fatal(err)
		}
		file.Close()
		modified := time.Now().Add(time.Duration(i-1) * time.Hour)
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
		names = append(names, path)
	}
	Prune(dir)
	if _, err := os.Stat(names[0]); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("oldest record survived storage budget")
	}
	if _, err := os.Stat(names[1]); err != nil {
		t.Fatal("newest record removed")
	}
}

func TestStoreOwnershipRetentionAndDeletion(t *testing.T) {
	_, c := fixtureCapture(t)
	c.Finish("stop", nil)
	record := savedRecord(t, c)
	if _, err := Read(c.dir, uuid.NewString(), record.RunID, record.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cross-session read: %v", err)
	}
	if _, err := Read(c.dir, record.SessionID, "../escape", record.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe read: %v", err)
	}
	path, _ := recordPath(record.SessionID, record.RunID, record.ID)
	absolute := filepath.Join(c.dir, directory, path)
	old := time.Now().Add(-(RetentionDays + 1) * 24 * time.Hour)
	if err := os.Chtimes(absolute, old, old); err != nil {
		t.Fatal(err)
	}
	Prune(c.dir)
	if _, err := os.Stat(absolute); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired trace retained: %v", err)
	}
	for i := 0; i < maxRecords+2; i++ {
		path, _ := recordPath(record.SessionID, record.RunID, uuid.NewString())
		full := filepath.Join(c.dir, directory, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("{}\n{}\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	Prune(c.dir)
	entries, err := os.ReadDir(filepath.Join(c.dir, directory, record.SessionID, record.RunID))
	if err != nil || len(entries) != maxRecords {
		t.Fatalf("retained count=%d err=%v", len(entries), err)
	}
	if err := DeleteSession(c.dir, record.SessionID); err != nil {
		t.Fatal(err)
	}
	if list, err := List(c.dir, record.SessionID, record.RunID); err != nil || len(list) != 0 {
		t.Fatalf("deleted session diagnostics: %+v %v", list, err)
	}
}
