package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/net/html/charset"
	"vista/internal/workerproto"
)

type WebPage struct {
	URL         string `json:"url"`
	ContentType string `json:"contentType"`
	Format      string `json:"format"`
	Content     string `json:"content"`
	Note        string `json:"note,omitempty"`
	Reader      string `json:"reader,omitempty"`
}

// Webfetch uses external reading services. Each candidate is decoded and fully
// staged on disk before it can become the selected artifact. Failed attempts
// cannot contaminate the saved document, and no full page is buffered in RAM.
func Webfetch(ctx context.Context, rawURL, format string, output io.Writer, settings workerproto.WebfetchConfig) (WebPage, error) {
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !webURLAllowed(req.URL.String()) {
			return errors.New("webfetch invalid redirect or more than five hops")
		}
		return nil
	}}
	return webfetchWithClient(ctx, rawURL, format, output, client, settings)
}

func webfetchWithClient(ctx context.Context, rawURL, format string, output io.Writer, client *http.Client, settings workerproto.WebfetchConfig) (WebPage, error) {
	if len(rawURL) > 1024 || !webURLAllowed(rawURL) {
		return WebPage{}, errors.New("webfetch requires an HTTP(S) URL without credentials")
	}
	if format != "" && format != "markdown" && format != "text" {
		return WebPage{}, errors.New("external readers return Markdown; HTML output is not supported")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return webfetchResponse(ctx, client, rawURL, output, settings)
}

func stageReader(ctx context.Context, response *http.Response) (*os.File, error) {
	defer response.Body.Close()
	if err := webfetchResponseError(response); err != nil {
		return nil, err
	}
	contentType, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil && response.Header.Get("Content-Type") != "" {
		return nil, fmt.Errorf("invalid content type: %w", err)
	}
	// Readers must return extracted text, rather than an origin HTML/PDF shell.
	if contentType != "" && contentType != "text/plain" && contentType != "text/markdown" && contentType != "text/x-markdown" {
		return nil, fmt.Errorf("reader returned unsupported content type %q", contentType)
	}
	label := params["charset"]
	if label == "" {
		label = "utf-8"
	}
	decoded, err := charset.NewReaderLabel(label, response.Body)
	if err != nil {
		return nil, fmt.Errorf("cannot decode charset: %w", err)
	}
	file, err := os.CreateTemp("", "vista-reader-*")
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			file.Close()
			os.Remove(file.Name())
		}
	}()
	// Validation scans the complete decoded document, including UTF-8 tails.
	var sample strings.Builder
	hasUnseenContent := false
	err = scanText(ctx, io.TeeReader(webContextReader{ctx, decoded}, file), func(fragment []byte, end bool) error {
		n := min(len(fragment), (64<<10)-sample.Len())
		sample.Write(fragment[:n])
		if strings.TrimSpace(string(fragment[n:])) != "" {
			hasUnseenContent = true
		}
		if end && sample.Len() < 64<<10 {
			sample.WriteByte('\n')
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reader download/decode failed: %w", err)
	}
	if err = readerContentError(sample.String(), !hasUnseenContent); err != nil {
		return nil, err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	keep = true
	return file, nil
}

func readerContentError(sample string, complete bool) error {
	lower := strings.ToLower(strings.TrimSpace(sample))
	body := lower
	if _, content, ok := strings.Cut(lower, "markdown content:"); ok {
		body = strings.TrimSpace(content)
	}
	if body == "" && complete {
		return errors.New("reader returned no document content")
	}
	preamble, _, _ := strings.Cut(lower, "markdown content:")
	for _, marker := range []string{"warning: target url returned error", "warning: this page maybe requiring captcha", "title: just a moment", "title: access denied", "<title>just a moment", "<title>attention required! | cloudflare", "<title>access denied", "<title>verify you are human"} {
		if strings.Contains(preamble, marker) {
			return errors.New("reader returned an error or browser verification page")
		}
	}
	if strings.HasPrefix(body, "<!doctype html") || strings.HasPrefix(body, "<html") {
		return errors.New("reader returned unextracted HTML")
	}
	// Only definite, short JS-only placeholders are failures; short useful pages
	// and articles discussing JavaScript remain valid documents.
	plain := strings.Trim(body, "#* \n\r\t.!-")
	for _, message := range []string{"please enable javascript", "you need to enable javascript to run this app", "javascript is required", "enable javascript to continue", "please turn on javascript"} {
		if complete && (plain == message || plain == message+".") {
			return errors.New("reader returned only a JavaScript-required placeholder")
		}
	}
	if strings.HasPrefix(body, `{"error":`) || strings.HasPrefix(body, `{"code":`) && strings.Contains(body, `"error"`) {
		return errors.New("reader returned an error envelope")
	}
	return nil
}

type webContextReader struct {
	ctx context.Context
	io.Reader
}

func (r webContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}
func webURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Hostname() != "" && u.User == nil && u.Fragment == "" && (u.Scheme == "http" || u.Scheme == "https")
}
