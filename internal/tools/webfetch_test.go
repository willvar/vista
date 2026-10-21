package tools

import (
	"context"
	"crypto/sha256"
	"errors"
	"hash"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"time"
	"vista/internal/workerproto"
)

func TestWebfetchFormatsAndLimits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<html><body><h1>Title</h1><p>你好 <a href="/next">next</a></p><script>secret()</script></body></html>`))
		case "/binary":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte{0, 1, 2})
		case "/redirect":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		}
	}))
	defer server.Close()
	page, err := webfetchTestPage(context.Background(), server.URL+"/html", "markdown")
	if err != nil || !strings.Contains(page.Content, "# Title") || !strings.Contains(page.Content, server.URL+"/next") {
		t.Fatalf("markdown conversion: page=%+v err=%v", page, err)
	}
	page, err = webfetchTestPage(context.Background(), server.URL+"/html", "text")
	if err != nil || !strings.Contains(page.Content, "Title") || !strings.Contains(page.Content, "你好 next") || strings.Contains(page.Content, "secret()") {
		t.Fatalf("plain text conversion: page=%+v err=%v", page, err)
	}
	page, err = webfetchTestPage(context.Background(), server.URL+"/html", "html")
	if err != nil || !strings.Contains(page.Content, "<h1>Title</h1>") {
		t.Fatalf("raw HTML: page=%+v err=%v", page, err)
	}
	for _, path := range []string{"/binary", "/redirect"} {
		if _, err := webfetchTestPage(context.Background(), server.URL+path, ""); err == nil {
			t.Fatalf("%s should have failed", path)
		}
	}
	if _, err := webfetchTestPage(context.Background(), "https://name:secret@example.com", ""); err == nil {
		t.Fatal("URL credentials were accepted")
	}
}

func TestWebfetchReturnsCompleteLargePage(t *testing.T) {
	content := strings.Repeat("你好 🙂 with evidence\n", 12000) + "final page evidence"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/page", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(content))
	}))
	defer server.Close()
	page, err := webfetchTestPage(context.Background(), server.URL+"/redirect", "text")
	if err != nil || page.Content != content || page.URL != server.URL+"/page" {
		t.Fatalf("large page: got %d bytes, URL %q, error %v", len(page.Content), page.URL, err)
	}
}

func TestWebfetchDecodesTextAndHTMLCharsets(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body, want, failure string
	}{
		{"gbk header", "text/html; charset=gbk", "<p>\xc4\xe3\xba\xc3</p>", "你好", ""},
		{"gbk meta", "text/html", `<meta charset="gbk"><p>` + "\xc4\xe3\xba\xc3</p>", "你好", ""},
		{"latin1 text", "text/plain; charset=iso-8859-1", "caf\xe9", "café", ""},
		{"xhtml", "application/xhtml+xml; charset=utf-8", "<p>你好</p>", "你好", ""},
		{"json", "application/json", `{"title":"你好"}`, `{"title":"你好"}`, ""},
		{"xml", "application/xml", "<feed>你好</feed>", "<feed>你好</feed>", ""},
		{"missing content type", "", "你好", "你好", ""},
		{"invalid UTF-8 replaced", "text/plain", "\xff\xfe", "��", ""},
		{"unknown charset", "text/plain; charset=nonexistent", "hello", "", "cannot decode charset"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header()["Content-Type"] = []string{tc.contentType}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			page, err := webfetchTestPage(context.Background(), server.URL, "text")
			if tc.failure != "" {
				if err == nil || !strings.Contains(err.Error(), tc.failure) || strings.Contains(err.Error(), "128 KiB") {
					t.Fatalf("expected %q, got %v", tc.failure, err)
				}
			} else if err != nil || page.Content != tc.want {
				t.Fatalf("decoded content %q, want %q; error %v", page.Content, tc.want, err)
			}
		})
	}
}

func TestWebfetchCancellationStopsDownload(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := webfetchTestPage(ctx, server.URL, "text")
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("webfetch did not stop after cancellation")
	}
}

func TestWebfetchStreamsBeyond50MiBWithoutWaitingForWholeResponse(t *testing.T) {
	chunk := []byte(strings.Repeat("streamed text ", 4096))
	const chunks = 1024 // 56 MiB, excluding the final marker.
	tail := []byte("FINAL PAGE EVIDENCE")
	expected := sha256.New()
	for range chunks {
		_, _ = expected.Write(chunk)
	}
	_, _ = expected.Write(tail)
	for _, contentLength := range []bool{true, false} {
		t.Run(strconv.FormatBool(contentLength), func(t *testing.T) {
			written := make(chan struct{})
			output := &webfetchTestWriter{Hash: sha256.New(), firstWrite: written}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				if contentLength {
					w.Header().Set("Content-Length", strconv.Itoa(chunks*len(chunk)+len(tail)))
				}
				for i := range chunks {
					if _, err := w.Write(chunk); err != nil {
						return
					}
					if i == 0 {
						w.(http.Flusher).Flush()
						// A whole-response buffer would deadlock here until the
						// test deadline: the consumer must write before EOF.
						select {
						case <-written:
						case <-r.Context().Done():
							return
						}
					}
				}
				_, _ = w.Write(tail)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := Webfetch(ctx, server.URL, "text", output, workerproto.WebfetchConfig{})
			if err != nil || output.bytes != chunks*len(chunk)+len(tail) || string(output.Sum(nil)) != string(expected.Sum(nil)) {
				t.Fatalf("streamed output: %d bytes, error %v", output.bytes, err)
			}
		})
	}
}

type webfetchTestWriter struct {
	hash.Hash
	bytes      int
	firstWrite chan struct{}
	once       sync.Once
}

func (w *webfetchTestWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.firstWrite) })
	n, err := w.Hash.Write(p)
	w.bytes += n
	return n, err
}

func TestWebfetchLargeHTMLPreservesSourceWithoutDOMConversion(t *testing.T) {
	body := "<html><body>" + strings.Repeat("完整 HTML 🙂\n", maxHTMLConversionBytes/12) + "FINAL HTML EVIDENCE</body></html>"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	page, err := webfetchTestPage(context.Background(), server.URL, "markdown")
	if err != nil || page.Content != body || page.Format != "html" || page.Note == "" {
		t.Fatalf("large HTML source: %d bytes, format %q, note %q, error %v", len(page.Content), page.Format, page.Note, err)
	}
}

func TestWebfetchInterruptedHTMLRetainsReceivedSource(t *testing.T) {
	for _, repeats := range []int{1, 2000} {
		t.Run(strconv.Itoa(repeats), func(t *testing.T) {
			body := "<p>" + strings.Repeat("received 中文", repeats)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Content-Length", strconv.Itoa(len(body)+1000))
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			page, err := webfetchTestPage(context.Background(), server.URL, "markdown")
			if err == nil || page.Content != body || page.Format != "html" || page.Note == "" {
				t.Fatalf("interrupted HTML: content=%q, format=%q, error=%v", page.Content, page.Format, err)
			}
		})
	}
}

func webfetchTestPage(ctx context.Context, rawURL, format string) (WebPage, error) {
	var output strings.Builder
	page, err := Webfetch(ctx, rawURL, format, &output, workerproto.WebfetchConfig{})
	page.Content = output.String()
	return page, err
}
