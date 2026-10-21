package server

import (
	"fmt"
	"os"
	"strings"

	"vista/internal/workerproto"
)

func searchProviders() ([]workerproto.SearchProvider, error) {
	defaults := []workerproto.SearchProvider{
		{Name: "firecrawl", Endpoint: "https://mcp.firecrawl.dev/v2/mcp"},
		{Name: "parallel", Endpoint: "https://search.parallel.ai/mcp"},
		{Name: "tavily", Endpoint: "https://api.tavily.com/search"},
		{Name: "exa", Endpoint: "https://mcp.exa.ai/mcp"},
		{Name: "deepseek", Endpoint: "https://api.deepseek.com/v1/tools/search"},
	}
	for i := range defaults {
		prefix := "VISTA_WEBSEARCH_" + strings.ToUpper(defaults[i].Name)
		if endpoint := os.Getenv(prefix + "_ENDPOINT"); endpoint != "" {
			defaults[i].Endpoint = endpoint
		}
		defaults[i].APIKey = os.Getenv(prefix + "_API_KEY")
		if defaults[i].APIKey == "" {
			defaults[i].APIKey = os.Getenv(strings.ToUpper(defaults[i].Name) + "_API_KEY")
		}
		if len(defaults[i].Endpoint) > 2048 || len(defaults[i].APIKey) > maxSearchAPIKeyBytes {
			return nil, fmt.Errorf("%s Web Search endpoint or key exceeds its size limit", defaults[i].Name)
		}
	}
	return defaults, nil
}
