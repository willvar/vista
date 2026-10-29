package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"vista/internal/useragent"
	"vista/internal/workerproto"
)

type webfetchTransport func(*http.Request) (*http.Response, error)

func (f webfetchTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func webfetchResponseFixture(r *http.Request, status int, contentType, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func TestWebfetchValidatesEveryAttemptBeforeSelection(t *testing.T) {
	for _, bad := range []struct{ name, contentType, body string }{
		{"empty", "text/plain", "  "},
		{"empty extracted body", "text/plain", "Title: Article\nMarkdown Content:\n "},
		{"challenge", "text/plain", "Title: Just a moment\nMarkdown Content:\nChecking your browser"},
		{"error", "text/plain", "Title: Missing\nWarning: Target URL returned error 404\nMarkdown Content:\nnot found"},
		{"JS shell", "text/markdown", "You need to enable JavaScript to run this app."},
		{"PDF", "application/pdf", "%PDF-1.7"},
		{"HTML shell", "text/html", "<html><div id=app></div></html>"},
		{"bad charset", "text/plain; charset=unknown-codepage", "body"},
	} {
		t.Run(bad.name, func(t *testing.T) {
			calls := []string{}
			wantedAgent, _ := useragent.Current()
			client := &http.Client{Transport: webfetchTransport(func(req *http.Request) (*http.Response, error) {
				calls = append(calls, req.URL.Hostname())
				if req.UserAgent() != wantedAgent {
					t.Fatal("selected UA lost")
				}
				if req.URL.Hostname() == "r.jina.ai" {
					if req.Header.Get("Authorization") != "Bearer jina-key" {
						t.Fatal("Jina key missing")
					}
					return webfetchResponseFixture(req, 200, bad.contentType, bad.body), nil
				}
				if req.URL.String() != deepSeekFetchEndpoint || req.Header.Get("Authorization") != "Bearer deep-key" {
					t.Fatal("request went to origin or wrong key")
				}
				body, _ := json.Marshal(map[string]string{"content": "OK"})
				return webfetchResponseFixture(req, 200, "application/json", string(body)), nil
			})}
			var output strings.Builder
			page, err := webfetchWithClient(context.Background(), "https://93.184.216.34/page", "", &output, client, workerproto.WebfetchConfig{JinaAPIKey: "jina-key", DeepSeekAPIKey: "deep-key"})
			if err != nil || page.Reader != "deepseek" || output.String() != "OK" || len(calls) != 2 {
				t.Fatalf("fallback=%+v calls=%v output=%q err=%v", page, calls, output.String(), err)
			}
		})
	}
}
func TestWebfetchAcceptsShortUsefulDocuments(t *testing.T) {
	for _, body := range []string{"OK", "# Status\nAll systems operational.", "Please enable JavaScript for interactive charts. The measured value is 42."} {
		client := &http.Client{Transport: webfetchTransport(func(r *http.Request) (*http.Response, error) {
			return webfetchResponseFixture(r, 200, "text/markdown", body), nil
		})}
		var output strings.Builder
		page, err := webfetchWithClient(context.Background(), "https://93.184.216.34/page", "", &output, client, workerproto.WebfetchConfig{})
		if err != nil || page.Reader != "jina" || output.String() != body {
			t.Fatalf("short valid page rejected: %v", err)
		}
	}
}
func TestWebfetchPrivateTargetsNeverReachExternalReaders(t *testing.T) {
	for _, target := range []string{"http://127.0.0.1/private", "http://10.0.0.1/", "http://192.168.1.2/", "http://host.docker.internal/", "https://93.184.216.34/a?token=secret", "https://user:pass@93.184.216.34/"} {
		client := &http.Client{Transport: webfetchTransport(func(*http.Request) (*http.Response, error) { t.Fatal("private target leaked"); return nil, nil })}
		if _, err := webfetchWithClient(context.Background(), target, "", io.Discard, client, workerproto.WebfetchConfig{DeepSeekAPIKey: "key"}); err == nil {
			t.Fatalf("accepted private target %s", target)
		}
	}
}
func TestWebfetchCancellationStopsFallbackAndKeepsCauses(t *testing.T) {
	for _, canceled := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		client := &http.Client{Transport: webfetchTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if canceled {
				cancel()
				return nil, ctx.Err()
			}
			return webfetchResponseFixture(r, 403, "text/plain", "blocked"), nil
		})}
		var output strings.Builder
		_, err := webfetchWithClient(ctx, "https://93.184.216.34/a", "", &output, client, workerproto.WebfetchConfig{DeepSeekAPIKey: "key"})
		if err == nil || output.Len() != 0 {
			t.Fatal("failed attempts became successful content")
		}
		if canceled {
			if calls != 1 || !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation started fallback")
			}
		} else if calls != 2 || !strings.Contains(err.Error(), "jina:") || !strings.Contains(err.Error(), "deepseek:") {
			t.Fatalf("per-reader causes missing: %v", err)
		}
	}
}
