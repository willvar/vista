package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"

	"vista/internal/instance"
	"vista/internal/network"
	"vista/internal/useragent"
)

// DOM conversion is bounded, not the download. Larger documents remain fully
// available as HTML artifacts instead of imposing a document truncation limit.
const localHTMLConversionBytes = 4 << 20

type webTarget struct {
	local        bool
	addresses    []net.IP
	allowPrivate bool
}

func sensitiveWebQuery(u *url.URL) bool {
	for key := range u.Query() {
		key = strings.ToLower(key)
		if strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "password") || strings.Contains(key, "signature") || key == "key" || key == "api_key" || key == "apikey" || key == "auth" || strings.HasPrefix(key, "x-amz-") || strings.HasPrefix(key, "x-goog-") {
			return true
		}
	}
	return false
}
func allowedLocalWebIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsPrivate() || netip.MustParsePrefix("100.64.0.0/10").Contains(ip) || publicWebReaderIP(ip)
}
func classifyWebTarget(ctx context.Context, raw string, explicitLocal bool) (webTarget, error) {
	u, err := url.Parse(raw)
	if err != nil || !webURLAllowed(raw) {
		return webTarget{}, errors.New("webfetch requires an HTTP(S) URL without embedded credentials")
	}
	if strings.EqualFold(u.Hostname(), "r.jina.ai") {
		return webTarget{}, errors.New("provide the original page URL, not a reader URL")
	}
	if sensitiveWebQuery(u) && !explicitLocal {
		return webTarget{}, errors.New("URL may contain authorization data; use explicit local reading only for authorized content")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		if !allowedLocalWebIP(ip) {
			return webTarget{}, errors.New("this address is not an ordinary HTTP page target")
		}
		local := explicitLocal || !publicWebReaderIP(ip)
		return webTarget{local: local, allowPrivate: local, addresses: []net.IP{net.IP(ip.AsSlice())}}, nil
	}
	allowPrivate := explicitLocal || network.IsLocalName(u.Hostname()) || strings.HasSuffix(strings.TrimSuffix(strings.ToLower(u.Hostname()), "."), ".test")
	resolution, err := network.LookupURL(ctx, u)
	if ctx.Err() != nil {
		return webTarget{}, ctx.Err()
	}
	if err != nil || len(resolution.Addresses) == 0 {
		return webTarget{}, errors.New("could not resolve page address; DNS failure is not a local-page classification")
	}
	for _, address := range resolution.Addresses {
		ip, ok := netip.AddrFromSlice(address)
		if !ok || !allowedLocalWebIP(ip) {
			return webTarget{}, errors.New("page resolves to a restricted address")
		}
		if !publicWebReaderIP(ip) && !allowPrivate {
			return webTarget{}, errors.New("domain resolves to a non-public address; explicitly choose local reading for an intended internal target")
		}
	}
	return webTarget{local: allowPrivate || !resolution.Complete, allowPrivate: allowPrivate, addresses: resolution.Addresses}, nil
}
func safeWebURL(u *url.URL) string {
	copy := *u
	query := copy.Query()
	if sensitiveWebQuery(&copy) {
		for key := range query {
			query.Set(key, "[redacted]")
		}
		copy.RawQuery = query.Encode()
	}
	return copy.String()
}
func webStageFile() (*os.File, error) {
	state, err := instance.Dir()
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(state, "web-staging")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	return os.CreateTemp(directory, "page-*")
}
func fetchLocalWeb(ctx context.Context, client *http.Client, raw string, format string, target webTarget, output io.Writer) (WebPage, error) {
	u, _ := url.Parse(raw)
	page := WebPage{URL: safeWebURL(u), Reader: "vista", Format: "text"}
	// Local fetching owns its redirect chain and never promotes a URL or response
	// to an external reader, including a private-to-public redirect.
	localClient := *client
	localClient.Jar = nil
	localClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("local webfetch exceeded five redirects")
		}
		next, err := classifyWebTarget(req.Context(), req.URL.String(), target.allowPrivate)
		if err != nil {
			return err
		}
		if !target.allowPrivate {
			for _, address := range next.addresses {
				ip, ok := netip.AddrFromSlice(address)
				if !ok || !publicWebReaderIP(ip) {
					return errors.New("automatic page reading cannot redirect to a non-public address")
				}
			}
		}
		bound, err := network.WithAddresses(req.Context(), req.URL.Hostname(), next.addresses)
		if err != nil {
			return err
		}
		*req = *req.WithContext(bound)
		previous := via[len(via)-1].URL
		if req.URL.Scheme != previous.Scheme || !strings.EqualFold(req.URL.Host, previous.Host) {
			req.Header.Del("Authorization")
			req.Header.Del("Cookie")
			req.Header.Del("Referer")
		}
		return nil
	}
	bound, err := network.WithAddresses(ctx, u.Hostname(), target.addresses)
	if err != nil {
		return page, err
	}
	request, err := http.NewRequestWithContext(bound, http.MethodGet, raw, nil)
	if err != nil {
		return page, errors.New("invalid local page request")
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml,text/markdown,text/plain,application/json,application/xml;q=0.9")
	response, err := useragent.Do(&localClient, request)
	if err != nil {
		var endpoint *url.Error
		if errors.As(err, &endpoint) {
			err = endpoint.Err
		}
		return page, fmt.Errorf("local page request failed: %w", err)
	}
	defer response.Body.Close()
	page.URL = safeWebURL(response.Request.URL)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return page, fmt.Errorf("local page returned HTTP %d", response.StatusCode)
	}
	contentType, parameters, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil {
		return page, errors.New("local page has an invalid content type")
	}
	htmlPage := contentType == "text/html" || contentType == "application/xhtml+xml"
	if !htmlPage && !strings.HasPrefix(contentType, "text/") && contentType != "application/json" && contentType != "application/xml" && contentType != "application/javascript" && !strings.HasSuffix(contentType, "+json") && !strings.HasSuffix(contentType, "+xml") {
		return page, errors.New("local page is not a supported text document; use a download tool for binary content")
	}
	var decoded io.Reader
	if htmlPage {
		decoded, err = charset.NewReader(response.Body, response.Header.Get("Content-Type"))
		if err == io.EOF {
			decoded, err = http.NoBody, nil
		}
		if err != nil {
			return page, fmt.Errorf("local HTML decoding failed: %w", err)
		}
	} else {
		label := parameters["charset"]
		if label == "" {
			label = "utf-8"
		}
		decoded, err = charset.NewReaderLabel(label, response.Body)
		if err != nil {
			return page, errors.New("unsupported local page charset")
		}
	}
	source := webContextReader{ctx, decoded}
	page.ContentType = contentType
	if htmlPage {
		prefix, err := io.ReadAll(io.LimitReader(source, localHTMLConversionBytes+1))
		if err != nil {
			return page, err
		}
		if len(prefix) > localHTMLConversionBytes {
			page.Format = "html"
			page.Note = strings.TrimSpace(page.Note + " Full HTML retained without DOM conversion because the document exceeds the conversion memory budget.")
			_, err = io.Copy(output, io.MultiReader(strings.NewReader(string(prefix)), source))
			return page, err
		}
		content := string(prefix)
		if format == "text" {
			node, err := html.Parse(strings.NewReader(content))
			if err != nil {
				return page, err
			}
			var text strings.Builder
			var walk func(*html.Node)
			walk = func(n *html.Node) {
				if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "noscript") {
					return
				}
				if n.Type == html.TextNode {
					text.WriteString(n.Data)
					text.WriteByte(' ')
				}
				for child := n.FirstChild; child != nil; child = child.NextSibling {
					walk(child)
				}
			}
			walk(node)
			content = strings.Join(strings.Fields(text.String()), " ")
		} else {
			content, err = htmltomarkdown.ConvertString(content, converter.WithDomain(response.Request.URL.String()))
			if err != nil {
				return page, err
			}
			page.Format = "markdown"
		}
		_, err = io.WriteString(output, content)
		return page, err
	}
	if strings.Contains(contentType, "markdown") {
		page.Format = "markdown"
	}
	_, err = io.Copy(output, source)
	return page, err
}
