package tools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"vista/internal/useragent"
	"vista/internal/workerproto"
)

// Choose a usable response before writing anything to the artifact. Failed
// attempts and challenge pages must never be prepended to recovered content.
func webfetchResponse(ctx context.Context, client *http.Client, rawURL, format string, settings workerproto.WebfetchConfig) (*http.Response, string, error) {
	order := settings.Order
	if order == nil {
		order = []string{"vista", "jina", "deepseek"}
	}
	seen := make(map[string]bool, len(order))
	for _, method := range order {
		if method != "vista" && method != "jina" && method != "deepseek" || seen[method] {
			return nil, "", errors.New("webfetch order must contain vista, jina, and deepseek exactly once")
		}
		seen[method] = true
	}
	if len(order) != 3 {
		return nil, "", errors.New("webfetch order must contain vista, jina, and deepseek exactly once")
	}
	var failures []error
	readerURL := rawURL
	for _, method := range order {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		var response *http.Response
		var err error
		name := "Vista built-in"
		if method == "vista" {
			response, err = webfetchDirectResponse(ctx, client, rawURL, format)
			if response != nil {
				readerURL = response.Request.URL.String()
			}
		} else if method == "deepseek" {
			if settings.DeepSeekAPIKey == "" {
				continue
			}
			name = "DeepSeek"
			if !publicWebReaderURL(ctx, rawURL) || !publicWebReaderURL(ctx, readerURL) {
				failures = append(failures, errors.New("DeepSeek: URL is not public or contains credentials"))
				continue
			}
			response, err = webfetchDeepSeekResponse(ctx, client, readerURL, settings)
			if err != nil {
				err = sanitizedSearchError(err, settings.DeepSeekAPIKey)
			}
		} else {
			name = "Jina Reader"
			// A remote reader is only for public, unauthenticated URLs, even
			// when the user has placed it first in the reading order.
			if !publicWebReaderURL(ctx, rawURL) || !publicWebReaderURL(ctx, readerURL) {
				failures = append(failures, errors.New("Jina Reader: URL is not public or contains credentials"))
				continue
			}
			response, err = webfetchGET(ctx, client, "https://r.jina.ai/"+readerURL, "markdown", settings.JinaAPIKey)
			if err == nil {
				err = webfetchResponseError(response, true)
				if err != nil {
					response.Body.Close()
				}
			}
		}
		if err == nil {
			return response, method, nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", name, err))
	}
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	return nil, "", fmt.Errorf("webfetch automatic reading attempts failed; use another source instead of repeating this URL unchanged: %w", errors.Join(failures...))
}

func webfetchDirectResponse(ctx context.Context, client *http.Client, rawURL, format string) (*http.Response, error) {
	response, err := webfetchGET(ctx, client, rawURL, format, "")
	if err != nil {
		return nil, err
	}
	err = webfetchResponseError(response, false)
	if err != nil {
		response.Body.Close()
	}
	// Keep the selected UA. The outer chain can try another reading service.
	return response, err
}

func webfetchGET(ctx context.Context, client *http.Client, rawURL, format, jinaAPIKey string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	accept := "text/html,application/xhtml+xml,application/json;q=0.9,application/xml;q=0.9,text/plain;q=0.8,*/*;q=0.5"
	if format == "markdown" {
		accept = "text/markdown," + accept
	}
	req.Header.Set("Accept", accept)
	if jinaAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+jinaAPIKey)
	}
	return useragent.Do(client, req)
}

func webfetchResponseError(response *http.Response, reader bool) error {
	if response.Header.Get("cf-mitigated") == "challenge" {
		return fmt.Errorf("webfetch HTTP status %d: website requires browser verification", response.StatusCode)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("webfetch HTTP status %d", response.StatusCode)
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if !reader && contentType != "" && !strings.Contains(contentType, "html") {
		return nil
	}
	// Inspect a prefix without consuming it or limiting the saved document.
	// Preserve read errors for the normal streaming path and partial artifact.
	body := bufio.NewReaderSize(response.Body, 8192)
	prefix, _ := body.Peek(8192)
	response.Body = webfetchReadCloser{Reader: body, Closer: response.Body}
	lower := strings.ToLower(string(prefix))
	if strings.Contains(lower, "<title>just a moment") ||
		strings.Contains(lower, "<title>attention required! | cloudflare") ||
		strings.Contains(lower, "<title>access denied") ||
		strings.Contains(lower, "<title>verify you are human") {
		return errors.New("webfetch received a browser verification page instead of the requested document")
	}
	if reader {
		preamble, _, _ := strings.Cut(lower, "markdown content:")
		if strings.Contains(preamble, "warning: target url returned error") ||
			strings.Contains(preamble, "warning: this page maybe requiring captcha") ||
			strings.HasPrefix(strings.TrimSpace(preamble), "title: just a moment") ||
			strings.HasPrefix(strings.TrimSpace(preamble), "title: access denied") {
			return errors.New("reader returned an error or verification page instead of the requested document")
		}
		if strings.TrimSpace(lower) == "" {
			return errors.New("reader returned no document content")
		}
	}
	return nil
}

type webfetchReadCloser struct {
	io.Reader
	io.Closer
}

func publicWebReaderURL(ctx context.Context, rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || !webURLAllowed(rawURL) || u.Hostname() == "r.jina.ai" {
		return false
	}
	for key := range u.Query() {
		key = strings.ToLower(key)
		if strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "password") || strings.Contains(key, "signature") || key == "key" || key == "api_key" || key == "apikey" || key == "auth" || strings.HasPrefix(key, "x-amz-") || strings.HasPrefix(key, "x-goog-") {
			return false
		}
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		return publicWebReaderIP(ip)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if !strings.Contains(host, ".") || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".test") {
		return false
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return false
	}
	for _, ip := range addresses {
		if !publicWebReaderIP(ip) {
			return false
		}
	}
	return true
}

func publicWebReaderIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !netip.MustParsePrefix("100.64.0.0/10").Contains(ip)
}
