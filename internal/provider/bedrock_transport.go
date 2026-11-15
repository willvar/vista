package provider

import (
	"context"
	"errors"
	"net/http"

	"vista/internal/network"
)

var bedrockHTTPClient = network.NewClient(http.Client{
	Transport: bedrockTransport{base: tracedModelHTTPTransport},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("Bedrock endpoint redirected")
	},
}, network.ConnectionScope)

type bedrockTransport struct{ base http.RoundTripper }

func (t bedrockTransport) Unwrap() http.RoundTripper { return t.base }

func (t bedrockTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(request.Context())
	response, err := t.base.RoundTrip(request.Clone(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	response.Body = &modelIdleBody{source: response.Body, cancel: cancel, idle: modelReadIdleTimeout}
	return response, nil
}
