package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"

	"vista/internal/useragent"
	"vista/internal/workerproto"
)

// Each reader must finish decoding and content validation before selection.
func webfetchResponse(ctx context.Context, client *http.Client, rawURL, format string, output io.Writer, settings workerproto.WebfetchConfig) (WebPage, error) {
	order := settings.Order
	if order == nil {
		order = []string{"jina", "deepseek"}
	}
	if len(order) != 2 || order[0] == order[1] {
		return WebPage{}, errors.New("webfetch order must contain jina and deepseek exactly once")
	}
	for _, method := range order {
		if method != "jina" && method != "deepseek" {
			return WebPage{}, errors.New("webfetch order must contain jina and deepseek exactly once")
		}
	}
	target, err := classifyWebTarget(ctx, rawURL, settings.Local)
	if err != nil {
		return WebPage{}, err
	}
	if target.local {
		return fetchLocalWeb(ctx, client, rawURL, format, target, output)
	}
	var failures []error
	for _, method := range order {
		if ctx.Err() != nil {
			return WebPage{}, ctx.Err()
		}
		if method == "deepseek" && settings.DeepSeekAPIKey == "" {
			continue
		}
		var response *http.Response
		var err error
		if method == "deepseek" {
			response, err = webfetchDeepSeekResponse(ctx, client, rawURL, settings)
		} else {
			response, err = webfetchGET(ctx, client, "https://r.jina.ai/"+rawURL, settings.JinaAPIKey)
		}
		if err == nil {
			var staged *os.File
			staged, err = stageReader(ctx, response)
			if err == nil {
				page := WebPage{URL: rawURL, ContentType: "text/markdown", Format: "markdown", Reader: method}
				_, copyErr := io.Copy(output, webContextReader{ctx, staged})
				staged.Close()
				os.Remove(staged.Name())
				return page, copyErr
			}
		}
		err = sanitizedSearchError(sanitizedSearchError(err, settings.JinaAPIKey), settings.DeepSeekAPIKey)
		failures = append(failures, fmt.Errorf("%s: %w", method, err))
	}
	if ctx.Err() != nil {
		return WebPage{}, ctx.Err()
	}
	return WebPage{}, fmt.Errorf("webfetch reading attempts failed; use another source instead of repeating this URL unchanged: %w", errors.Join(failures...))
}
func webfetchGET(ctx context.Context, client *http.Client, rawURL, key string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/markdown,text/plain;q=0.9")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return useragent.Do(client, req)
}
func webfetchResponseError(response *http.Response) error {
	if response.Header.Get("cf-mitigated") == "challenge" {
		return errors.New("reader requires browser verification")
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("webfetch HTTP status %d", response.StatusCode)
	}
	return nil
}

func publicWebReaderURL(ctx context.Context, rawURL string) bool {
	target, err := classifyWebTarget(ctx, rawURL, false)
	return err == nil && !target.local
}

func publicWebReaderIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !netip.MustParsePrefix("100.64.0.0/10").Contains(ip)
}
