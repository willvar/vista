package tools

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"vista/internal/useragent"
	"vista/internal/workerproto"
)

type webfetchTransport func(*http.Request) (*http.Response, error)

func (f webfetchTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func webfetchResponseFixture(request *http.Request, status int, contentType, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func TestWebfetchUsesConfiguredIdentityAndFallsBackWithoutChangingIt(t *testing.T) {
	wantAgent, err := useragent.Current()
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := &http.Client{Transport: webfetchTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Header.Get("Accept-Language") == "" || !strings.Contains(req.Header.Get("Accept"), "text/html") || req.UserAgent() != wantAgent {
			t.Fatal("missing request headers or selected UA")
		}
		if calls == 1 {
			return webfetchResponseFixture(req, 403, "text/html", "blocked"), nil
		}
		if req.URL.Hostname() != "r.jina.ai" {
			t.Fatal("blocked origin was retried instead of trying the next service")
		}
		return webfetchResponseFixture(req, 200, "text/plain; charset=utf-8", "Title: Recovered article\n\nMarkdown Content:\n正文"), nil
	})}
	var output strings.Builder
	page, err := webfetchWithClient(context.Background(), "https://93.184.216.34/article", "markdown", &output, client, workerproto.WebfetchConfig{})
	if err != nil || calls != 2 || page.Reader != "jina" || !strings.Contains(output.String(), "正文") || strings.Contains(output.String(), "blocked") {
		t.Fatalf("recovery: calls=%d, page=%+v, output=%q, err=%v", calls, page, output.String(), err)
	}
}

func TestWebfetchReaderFallbackPreservesEntireDocument(t *testing.T) {
	full := "Title: Recovered page\n\nMarkdown Content:\n" + strings.Repeat("完整正文 🙂\n", 18000) + "FINAL EVIDENCE"
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: webfetchTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Hostname() != "r.jina.ai" {
					return webfetchResponseFixture(req, status, "text/html", "<title>Just a moment...</title>Enable JavaScript and cookies to continue"), nil
				}
				if req.URL.String() != "https://r.jina.ai/https://93.184.216.34/article?id=42" || req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
					t.Fatal("reader target or credentials are incorrect")
				}
				return webfetchResponseFixture(req, 200, "text/plain; charset=utf-8", full), nil
			})}
			var output strings.Builder
			page, err := webfetchWithClient(context.Background(), "https://93.184.216.34/article?id=42", "text", &output, client, workerproto.WebfetchConfig{})
			wantCalls := 2
			if err != nil || calls != wantCalls || page.Reader != "jina" || page.Format != "markdown" || page.URL != "https://93.184.216.34/article?id=42" || output.String() != full {
				t.Fatalf("reader fallback: calls=%d, page=%+v, bytes=%d, error=%v", calls, page, output.Len(), err)
			}
		})
	}
}

func TestWebfetchReaderKeyIsSentOnlyToJina(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: webfetchTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Hostname() == "r.jina.ai" {
			if req.Header.Get("Authorization") != "Bearer jina-secret" {
				t.Fatalf("Jina request did not receive the configured key: %q", req.Header.Get("Authorization"))
			}
			return webfetchResponseFixture(req, http.StatusOK, "text/plain; charset=utf-8", "Title: Recovered\n\nMarkdown Content:\n正文"), nil
		}
		if req.Header.Get("Authorization") != "" {
			t.Fatal("Jina key leaked to the origin request")
		}
		return webfetchResponseFixture(req, http.StatusForbidden, "text/html", "Forbidden"), nil
	})}
	var output strings.Builder
	page, err := webfetchWithClient(context.Background(), "https://93.184.216.34/article", "markdown", &output, client, workerproto.WebfetchConfig{JinaAPIKey: "jina-secret"})
	if err != nil || calls != 2 || page.Reader != "jina" || output.String() == "" {
		t.Fatalf("Jina key fallback: calls=%d, page=%+v, output=%q, err=%v", calls, page, output.String(), err)
	}
}

func TestWebfetchDoesNotAcceptReaderErrorPagesAsSuccess(t *testing.T) {
	for _, body := range []string{
		"Title: Just a moment...\n\nMarkdown Content:\nVerify you are human",
		"Title: site\n\nWarning: This page maybe requiring CAPTCHA, please make sure you are authorized to access this page.\n\nMarkdown Content:\nsecurity verification",
		"Title: missing\n\nWarning: Target URL returned error 404: Not Found\n\nMarkdown Content:\nnot found",
		"  \n",
	} {
		client := &http.Client{Transport: webfetchTransport(func(req *http.Request) (*http.Response, error) {
			if req.URL.Hostname() == "r.jina.ai" {
				return webfetchResponseFixture(req, 200, "text/plain", body), nil
			}
			return webfetchResponseFixture(req, 403, "text/html", "Forbidden"), nil
		})}
		var output strings.Builder
		_, err := webfetchWithClient(context.Background(), "https://93.184.216.34/article", "markdown", &output, client, workerproto.WebfetchConfig{})
		if err == nil || !strings.Contains(err.Error(), "Jina Reader:") || !strings.Contains(err.Error(), "use another source") || output.Len() != 0 {
			t.Fatalf("false success: bytes=%d, error=%v", output.Len(), err)
		}
	}
}

func TestWebfetchDefinitiveErrorsTryNextMethod(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusGone} {
		calls := 0
		client := &http.Client{Transport: webfetchTransport(func(req *http.Request) (*http.Response, error) {
			calls++
			return webfetchResponseFixture(req, status, "text/html", "unavailable"), nil
		})}
		_, err := webfetchWithClient(context.Background(), "https://93.184.216.34/article", "markdown", io.Discard, client, workerproto.WebfetchConfig{})
		if err == nil || calls != 2 {
			t.Fatalf("status %d: calls=%d, error=%v", status, calls, err)
		}
	}
}

func TestWebfetchDoesNotSendPrivateTargetsToReader(t *testing.T) {
	for _, target := range []string{
		"http://127.0.0.1/private", "http://10.0.0.1/private", "http://[::1]/private", "http://[::ffff:127.0.0.1]/private",
		"http://100.64.0.1/private", "http://192.168.1.1/private", "http://169.254.169.254/private",
		"http://service.internal/private", "http://localhost/private", "http://service/private",
		"https://93.184.216.34/private?access_token=credential", "https://93.184.216.34/private?X-Amz-Signature=credential",
	} {
		calls := 0
		client := &http.Client{Transport: webfetchTransport(func(req *http.Request) (*http.Response, error) {
			calls++
			if req.URL.Hostname() == "r.jina.ai" {
				t.Fatal("private target was sent to remote reader")
			}
			return webfetchResponseFixture(req, 403, "text/html", "Forbidden"), nil
		})}
		_, err := webfetchWithClient(context.Background(), target, "markdown", io.Discard, client, workerproto.WebfetchConfig{})
		if err == nil || calls != 1 {
			t.Fatalf("target %q: calls=%d, error=%v", target, calls, err)
		}
	}
}

func TestWebfetchCancellationDoesNotStartFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	client := &http.Client{Transport: webfetchTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		cancel()
		return webfetchResponseFixture(req, 403, "text/html", "Forbidden"), nil
	})}
	_, err := webfetchWithClient(ctx, "https://93.184.216.34/article", "markdown", io.Discard, client, workerproto.WebfetchConfig{})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancellation: calls=%d, error=%v", calls, err)
	}
}

func TestWebfetchFollowsConfiguredReadingOrder(t *testing.T) {
	for _, first := range []string{"vista", "jina"} {
		for _, failFirst := range []bool{false, true} {
			t.Run(first+map[bool]string{false: " succeeds", true: " fails"}[failFirst], func(t *testing.T) {
				order := []string{"vista", "jina", "deepseek"}
				if first == "jina" {
					order[0], order[1] = order[1], order[0]
				}
				var calls []string
				client := &http.Client{Transport: webfetchTransport(func(req *http.Request) (*http.Response, error) {
					method := "vista"
					if req.URL.Hostname() == "r.jina.ai" {
						method = "jina"
						if req.Header.Get("Authorization") != "Bearer jina-secret" {
							t.Fatal("Jina did not receive its configured key")
						}
					} else if req.Header.Get("Authorization") != "" {
						t.Fatal("Jina key leaked to the origin")
					}
					calls = append(calls, method)
					if method == first && failFirst {
						return nil, errors.New("request unavailable")
					}
					return webfetchResponseFixture(req, http.StatusOK, "text/plain; charset=utf-8", method+" full document"), nil
				})}
				var output strings.Builder
				page, err := webfetchWithClient(context.Background(), "https://93.184.216.34/article", "markdown", &output, client, workerproto.WebfetchConfig{Order: order, JinaAPIKey: "jina-secret"})
				want := first
				count := 1
				if failFirst {
					want, count = order[1], 2
				}
				if err != nil || len(calls) != count || calls[0] != first || page.Reader != want || output.String() != want+" full document" {
					t.Fatalf("reading order: calls=%v page=%+v output=%q err=%v", calls, page, output.String(), err)
				}
				if first == "jina" && !failFirst && strings.Contains(page.Note, "failed") {
					t.Fatal("Jina-first success must not claim direct access failed")
				}
			})
		}
	}
}

func TestWebfetchJinaFirstRejectsErrorPagesAndUsesVista(t *testing.T) {
	var calls []string
	client := &http.Client{Transport: webfetchTransport(func(req *http.Request) (*http.Response, error) {
		calls = append(calls, req.URL.Hostname())
		if req.URL.Hostname() == "r.jina.ai" {
			return webfetchResponseFixture(req, http.StatusOK, "text/plain", "Title: Missing\nWarning: Target URL returned error 404\nMarkdown Content:\nnot found"), nil
		}
		return webfetchResponseFixture(req, http.StatusOK, "text/html", "<p>Actual document</p>"), nil
	})}
	var output strings.Builder
	page, err := webfetchWithClient(context.Background(), "https://93.184.216.34/article", "text", &output, client, workerproto.WebfetchConfig{Order: []string{"jina", "vista", "deepseek"}})
	if err != nil || len(calls) != 2 || calls[0] != "r.jina.ai" || page.Reader != "vista" || output.String() != "Actual document" {
		t.Fatalf("reader error fallback: calls=%v page=%+v output=%q err=%v", calls, page, output.String(), err)
	}
}

func TestWebfetchJinaFirstKeepsPrivateTargetsLocal(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: webfetchTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Hostname() != "127.0.0.1" {
			t.Fatal("private target sent to Jina")
		}
		return webfetchResponseFixture(req, http.StatusOK, "text/plain", "Local document"), nil
	})}
	var output strings.Builder
	page, err := webfetchWithClient(context.Background(), "http://127.0.0.1/private", "text", &output, client, workerproto.WebfetchConfig{Order: []string{"jina", "vista", "deepseek"}})
	if err != nil || calls != 1 || page.Reader != "vista" || output.String() != "Local document" {
		t.Fatalf("local fallback: calls=%d page=%+v err=%v", calls, page, err)
	}
}

func TestWebfetchJinaCancellationDoesNotStartVista(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	client := &http.Client{Transport: webfetchTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		cancel()
		return nil, context.Canceled
	})}
	_, err := webfetchWithClient(ctx, "https://93.184.216.34/article", "text", io.Discard, client, workerproto.WebfetchConfig{Order: []string{"jina", "vista", "deepseek"}})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancellation: calls=%d err=%v", calls, err)
	}
}
