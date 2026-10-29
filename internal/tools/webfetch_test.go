package tools

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"vista/internal/workerproto"
)

func TestWebfetchCompleteDocumentDecodesBeforeSelection(t *testing.T) {
	for _, tc := range []struct{ contentType, body, want string }{
		{"text/markdown", strings.Repeat(" ", 70000) + "Useful evidence", strings.Repeat(" ", 70000) + "Useful evidence"},
		{"text/markdown", "Title: Article\nMarkdown Content:\n" + strings.Repeat(" ", 70000) + "Later evidence", "Title: Article\nMarkdown Content:\n" + strings.Repeat(" ", 70000) + "Later evidence"},
		{"text/plain; charset=iso-8859-1", "caf\xe9", "café"},
		{"text/markdown; charset=gbk", "\xc4\xe3\xba\xc3", "你好"},
		{"text/markdown; charset=utf-8", strings.Repeat("证据🙂\n", 10000), strings.Repeat("证据🙂\n", 10000)},
	} {
		client := &http.Client{Transport: webfetchTransport(func(r *http.Request) (*http.Response, error) {
			return webfetchResponseFixture(r, 200, tc.contentType, tc.body), nil
		})}
		var output strings.Builder
		page, err := webfetchWithClient(context.Background(), "https://93.184.216.34/article", "", &output, client, workerproto.WebfetchConfig{})
		if err != nil || page.Format != "markdown" || output.String() != tc.want {
			t.Fatalf("decode: %v", err)
		}
	}
}
func TestWebfetchStreamsLargeReaderWithoutFullPageAllocation(t *testing.T) {
	const bytes = 55 << 20
	client := &http.Client{Transport: webfetchTransport(func(r *http.Request) (*http.Response, error) {
		response := webfetchResponseFixture(r, 200, "text/markdown", "")
		response.Body = io.NopCloser(io.LimitReader(repeatWebReader{}, bytes))
		return response, nil
	})}
	digest := sha256.New()
	page, err := webfetchWithClient(context.Background(), "https://93.184.216.34/large", "", digest, client, workerproto.WebfetchConfig{})
	if err != nil || page.Reader != "jina" {
		t.Fatal(err)
	}
	expected := sha256.New()
	io.Copy(expected, io.LimitReader(repeatWebReader{}, bytes))
	if string(digest.Sum(nil)) != string(expected.Sum(nil)) {
		t.Fatal("large document truncated")
	}
}

type repeatWebReader struct{}

func (repeatWebReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

type brokenWebReader struct{ sent bool }

func (r *brokenWebReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, errors.New("lost connection")
	}
	r.sent = true
	return copy(p, "partial failed document"), nil
}
func TestWebfetchInterruptedCandidateDoesNotPolluteNext(t *testing.T) {
	client := &http.Client{Transport: webfetchTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "r.jina.ai" {
			response := webfetchResponseFixture(r, 200, "text/plain", "")
			response.Body = io.NopCloser(&brokenWebReader{})
			return response, nil
		}
		return webfetchResponseFixture(r, 200, "application/json", `{"content":"Complete recovered page"}`), nil
	})}
	var output strings.Builder
	page, err := webfetchWithClient(context.Background(), "https://93.184.216.34/a", "", &output, client, workerproto.WebfetchConfig{DeepSeekAPIKey: "key"})
	if err != nil || page.Reader != "deepseek" || output.String() != "Complete recovered page" {
		t.Fatalf("partial candidate selected: %+v %v", page, err)
	}
}
