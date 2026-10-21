package provider

import (
	"fmt"
	"net/url"
)

type citationSources struct {
	urls  []string
	seen  map[string]bool
	bytes int
}

func (c *citationSources) add(source, origin string) error {
	if c.seen[source] {
		return nil
	}
	// Provider search can return more citations than Vista exposes. Once the
	// bounded set is full, keep parsing the response and ignore later sources;
	// an otherwise valid model response must not fail just because it cited a
	// seventeenth URL.
	if len(c.urls) >= 16 {
		return nil
	}
	parsed, err := url.Parse(source)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || len(source) > 2048 || len(source) > 8192-c.bytes {
		return fmt.Errorf("%s citations exceed limits or contain an invalid URL", origin)
	}
	if c.seen == nil {
		c.seen = map[string]bool{}
	}
	c.seen[source] = true
	c.urls = append(c.urls, source)
	c.bytes += len(source)
	return nil
}
