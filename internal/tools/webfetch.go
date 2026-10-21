package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"vista/internal/workerproto"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// HTML conversion builds a DOM. Larger pages are still saved completely, as
// HTML, so neither the download nor the saved artifact needs a size limit.
const maxHTMLConversionBytes = 4 << 20

type WebPage struct {
	URL         string `json:"url"`
	ContentType string `json:"contentType"`
	Format      string `json:"format"`
	Content     string `json:"content"`
	Note        string `json:"note,omitempty"`
	Reader      string `json:"reader,omitempty"`
}

// Webfetch streams decoded text to output. Callers own its storage and expose
// ranges of that snapshot instead of fetching the URL again for each page.
func Webfetch(ctx context.Context, rawURL, format string, output io.Writer, settings workerproto.WebfetchConfig) (WebPage, error) {
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !webURLAllowed(req.URL.String()) {
			return errors.New("webfetch redirect is invalid or exceeds five hops")
		}
		return nil
	}}
	return webfetchWithClient(ctx, rawURL, format, output, client, settings)
}

func webfetchWithClient(ctx context.Context, rawURL, format string, output io.Writer, client *http.Client, settings workerproto.WebfetchConfig) (WebPage, error) {
	if len(rawURL) > 1024 || !webURLAllowed(rawURL) {
		return WebPage{}, errors.New("webfetch requires an HTTP(S) URL without credentials")
	}
	if format == "" {
		format = "markdown"
	}
	if format != "markdown" && format != "text" && format != "html" {
		return WebPage{}, errors.New("webfetch format must be markdown, text, or html")
	}
	// Each HTTP request keeps its own timeout; the entire fallback chain also
	// has a deadline so unavailable readers cannot prolong a tool indefinitely.
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	response, reader, err := webfetchResponse(ctx, client, rawURL, format, settings)
	if err != nil {
		return WebPage{}, err
	}
	defer response.Body.Close()
	contentTypeHeader := response.Header.Get("Content-Type")
	contentType, params, err := mime.ParseMediaType(contentTypeHeader)
	if err != nil && contentTypeHeader != "" {
		return WebPage{}, fmt.Errorf("webfetch invalid content type: %w", err)
	}
	if contentType != "" && !webTextContentType(contentType) {
		return WebPage{}, fmt.Errorf("webfetch unsupported content type %q", contentType)
	}
	body := bufio.NewReader(response.Body)
	if contentType == "" {
		prefix, err := body.Peek(512)
		if err != nil && err != io.EOF && len(prefix) == 0 {
			return WebPage{}, err
		}
		contentType, _, _ = mime.ParseMediaType(http.DetectContentType(prefix))
		if !webTextContentType(contentType) {
			return WebPage{}, fmt.Errorf("webfetch unsupported content type %q", contentType)
		}
	}
	isHTML := contentType == "text/html" || contentType == "application/xhtml+xml"
	var decoded io.Reader
	if isHTML {
		// HTML encoding may be supplied in either the HTTP header or a meta tag.
		// Peek keeps an interrupted download's error on the original reader;
		// charset.NewReader treats an early unexpected EOF as a complete page.
		prefix, peekErr := body.Peek(1024)
		if peekErr != nil && peekErr != io.EOF && len(prefix) == 0 {
			return WebPage{}, peekErr
		}
		_, label, _ := charset.DetermineEncoding(prefix, contentTypeHeader)
		decoded, err = charset.NewReaderLabel(label, body)
	} else {
		label := params["charset"]
		if label == "" {
			label = "utf-8"
		}
		decoded, err = charset.NewReaderLabel(label, body)
	}
	if err != nil {
		return WebPage{}, fmt.Errorf("webfetch cannot decode charset: %w", err)
	}
	page := WebPage{URL: response.Request.URL.String(), ContentType: contentType, Format: format, Reader: reader}
	if reader == "jina" {
		page.URL, page.Reader, page.Format = rawURL, reader, "markdown"
		page.Note = "Fetched through Jina Reader; the saved content is Markdown."
	}
	if reader == "deepseek" {
		page.URL, page.Format = rawURL, "markdown"
		page.Note = "Fetched through DeepSeek; the saved content is Markdown."
	}
	decoded = webContextReader{ctx, decoded}
	if isHTML && format != "html" {
		prefix, err := io.ReadAll(io.LimitReader(decoded, maxHTMLConversionBytes+1))
		if err != nil {
			page.Format = "html"
			page.Note = "Download interrupted; the received HTML source is saved as an incomplete artifact."
			_, writeErr := io.Copy(output, bytes.NewReader(prefix))
			return page, errors.Join(err, writeErr)
		}
		if len(prefix) > maxHTMLConversionBytes {
			page.Format = "html"
			page.Note = "Saved the full HTML source. This page is too large to convert in memory; read selected ranges from the saved artifact."
			_, err := io.Copy(output, io.MultiReader(bytes.NewReader(prefix), decoded))
			return page, err
		}
		content := string(prefix)
		switch format {
		case "markdown":
			converted, err := htmltomarkdown.ConvertString(content, converter.WithDomain(response.Request.URL.String()))
			if err != nil {
				return page, err
			}
			content = converted
		case "text":
			node, err := html.Parse(strings.NewReader(content))
			if err != nil {
				return page, err
			}
			var out strings.Builder
			var walk func(*html.Node)
			walk = func(n *html.Node) {
				if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
					return
				}
				if n.Type == html.TextNode {
					out.WriteString(n.Data)
					out.WriteByte(' ')
				}
				for child := n.FirstChild; child != nil; child = child.NextSibling {
					walk(child)
				}
			}
			walk(node)
			content = strings.Join(strings.Fields(out.String()), " ")
		}
		decoded = webContextReader{ctx, strings.NewReader(content)}
	}
	_, err = io.Copy(output, decoded)
	return page, err
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

func webTextContentType(contentType string) bool {
	return strings.HasPrefix(contentType, "text/") || contentType == "application/json" || strings.HasSuffix(contentType, "+json") || contentType == "application/xml" || strings.HasSuffix(contentType, "+xml") || contentType == "application/javascript"
}

func webURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Hostname() != "" && u.User == nil && u.Fragment == "" && (u.Scheme == "http" || u.Scheme == "https")
}
