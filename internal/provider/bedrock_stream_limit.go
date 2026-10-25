package provider

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"strings"
)

const maxBedrockFrameBytes = maxEventBytes + 4096
const maxBedrockStreamBytes = 1 << 20

var bedrockHTTPClient = &http.Client{
	Transport: bedrockTransport{base: tracedModelHTTPTransport},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("Bedrock endpoint redirected")
	},
}

type bedrockTransport struct{ base http.RoundTripper }

func (t bedrockTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(request.Context())
	response, err := t.base.RoundTrip(request.Clone(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	response.Body = &modelIdleBody{source: response.Body, cancel: cancel, idle: modelReadIdleTimeout}
	if response.StatusCode == http.StatusOK && strings.HasPrefix(response.Header.Get("Content-Type"), "application/vnd.amazon.eventstream") {
		response.Body = &limitedBedrockBody{source: response.Body, reader: bufio.NewReader(response.Body)}
	}
	return response, nil
}

// Reject the frame length before the SDK decoder sees it and allocates a frame.
type limitedBedrockBody struct {
	source    io.ReadCloser
	reader    *bufio.Reader
	remaining uint32
	total     uint32
}

func (b *limitedBedrockBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.remaining == 0 {
		prefix, err := b.reader.Peek(4)
		if err != nil {
			return 0, err
		}
		length := binary.BigEndian.Uint32(prefix)
		if length < 16 || length > maxBedrockFrameBytes || b.total+length > maxBedrockStreamBytes {
			return 0, errors.New("Bedrock stream frame or total exceeds limits")
		}
		b.remaining = length
		b.total += length
	}
	if uint32(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.reader.Read(p)
	b.remaining -= uint32(n)
	return n, err
}

func (b *limitedBedrockBody) Close() error { return b.source.Close() }
