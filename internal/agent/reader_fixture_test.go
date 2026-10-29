package agent

import (
	"net/http"
	"net/url"
	"testing"
)

type readerTestTransport func(*http.Request) (*http.Response, error)

func (f readerTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Tests exercise the external-reader contract using an isolated local HTTP
// fixture without weakening production public-target validation.
func routeReaderFixture(t *testing.T, endpoint string) {
	t.Helper()
	target, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultTransport
	http.DefaultTransport = readerTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() != "r.jina.ai" {
			return previous.RoundTrip(r)
		}
		next := r.Clone(r.Context())
		u := *r.URL
		u.Scheme = target.Scheme
		u.Host = target.Host
		u.Path = target.Path
		u.RawQuery = target.RawQuery
		next.URL = &u
		next.Host = target.Host
		return previous.RoundTrip(next)
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
}
