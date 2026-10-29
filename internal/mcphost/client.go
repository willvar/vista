package mcphost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"vista/internal/mcpclient"
	"vista/internal/workerproto"
)

// Lease belongs to an execution including its detached children. Closing one
// proxy or canceling one tool request never closes another caller's connection.
type Lease struct {
	host           workerproto.MCPHost
	id             string
	stateDir       string
	keys           map[string]bool
	client         *http.Client
	transport      *http.Transport
	leaseTransport *http.Transport
	body           io.ReadCloser
	cancel         context.CancelFunc
	done           chan struct{}
	once           sync.Once
}

func Acquire(ctx context.Context, host workerproto.MCPHost, stateDir string, definitions []Definition) (*Lease, error) {
	ctx, cancel := context.WithCancel(ctx)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 70 * time.Second
	transport.MaxIdleConnsPerHost = 16
	leaseTransport := transport.Clone()
	leaseTransport.ResponseHeaderTimeout = time.Second
	l := &Lease{host: host, stateDir: stateDir, keys: map[string]bool{}, client: &http.Client{Transport: transport}, transport: transport, leaseTransport: leaseTransport, cancel: cancel, done: make(chan struct{})}
	for _, definition := range definitions {
		l.keys[Key(definition)] = true
	}
	response, err := l.post(ctx, "/lease", request{Definitions: definitions})
	if err != nil {
		cancel()
		transport.CloseIdleConnections()
		leaseTransport.CloseIdleConnections()
		return nil, err
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 128))
	if err := decoder.Decode(&l.id); err != nil || len(l.id) != 36 {
		cancel()
		_ = response.Body.Close()
		transport.CloseIdleConnections()
		leaseTransport.CloseIdleConnections()
		return nil, errors.New("invalid MCP host lease")
	}
	l.body = response.Body
	go func() {
		_, _ = io.Copy(io.Discard, response.Body)
		close(l.done)
	}()
	return l, nil
}

func (l *Lease) Context(ctx context.Context) context.Context {
	return mcpclient.WithConnector(ctx, l.Connect)
}

func (l *Lease) Connect(_ context.Context, config mcpclient.Config, search bool) (mcpclient.Connection, error) {
	key := Key(Definition{Config: config, Search: search})
	if !l.keys[key] {
		return nil, errors.New("MCP connection is outside this execution snapshot")
	}
	return &proxy{lease: l, key: key}, nil
}

func (l *Lease) Close() error {
	l.once.Do(func() {
		l.cancel()
		_ = l.body.Close()
		<-l.done
		l.transport.CloseIdleConnections()
		l.leaseTransport.CloseIdleConnections()
	})
	return nil
}

func (l *Lease) post(ctx context.Context, path string, input request) (*http.Response, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.host.Endpoint+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+l.host.Token)
	client := l.client
	if path == "/lease" {
		client = &http.Client{Transport: l.leaseTransport}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("MCP host connection interrupted; call was not replayed: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		_ = res.Body.Close()
		return nil, fmt.Errorf("MCP host returned HTTP %d; call was not replayed", res.StatusCode)
	}
	return res, nil
}

func (l *Lease) call(ctx context.Context, path string, input request, dst any) error {
	select {
	case <-l.done:
		return errors.New("MCP execution connection was interrupted; call was not replayed")
	default:
	}
	ctx, cancel := context.WithTimeout(ctx, 70*time.Second)
	defer cancel()
	input.Lease = l.id
	res, err := l.post(ctx, path, input)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var out response
	if err := json.NewDecoder(io.LimitReader(res.Body, maxControlBytes)).Decode(&out); err != nil {
		return fmt.Errorf("read MCP host response: %w", err)
	}
	if out.Error != "" {
		return errors.New(out.Error)
	}
	if out.File == "" {
		return json.Unmarshal(out.Data, dst)
	}
	root, err := os.OpenRoot(filepath.Join(l.stateDir, "mcp-host-data"))
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.Open(out.File)
	if err != nil {
		return err
	}
	defer file.Close()
	defer root.Remove(out.File)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != out.Bytes {
		return errors.New("invalid MCP result file")
	}
	return json.NewDecoder(file).Decode(dst)
}

// Inspect bounds discovery in the host, rather than making a separate MCP
// connection in the Server. Tool data travels only in this on-demand response.
func (l *Lease) Inspect(ctx context.Context, config mcpclient.Config) ([]mcpclient.Tool, error) {
	var tools []mcpclient.Tool
	err := l.call(ctx, "/inspect", request{Key: Key(Definition{Config: config}), Force: true}, &tools)
	return tools, err
}

type proxy struct {
	lease *Lease
	key   string
}

func (p *proxy) ListTools(ctx context.Context, cursor string) (mcpclient.ToolPage, error) {
	var page mcpclient.ToolPage
	err := p.lease.call(ctx, "/list", request{Key: p.key, Cursor: cursor}, &page)
	return page, err
}

func (p *proxy) CallTool(ctx context.Context, name string, args json.RawMessage) (mcpclient.CallResult, error) {
	var result mcpclient.CallResult
	err := p.lease.call(ctx, "/call", request{Key: p.key, Name: name, Arguments: args}, &result)
	return result, err
}

func (*proxy) Close() error { return nil }
