package mcphost

import (
	"context"
	"errors"
	"sync"
	"time"

	"vista/internal/mcpclient"
	"vista/internal/workerproto"
)

type endpointResolverKey struct{}
type EndpointResolver func(context.Context) (workerproto.MCPHost, error)

func WithEndpointResolver(ctx context.Context, resolve EndpointResolver) context.Context {
	return context.WithValue(ctx, endpointResolverKey{}, resolve)
}

// Execution reserves configuration versions before model execution when the
// host is ready; this local exchange does not wait for upstream handshakes.
// When the host is unavailable, only actual MCP calls wait for its readiness.
// Detached children keep the handle until their execution scope is released.
type Execution struct {
	ready  chan struct{}
	cancel context.CancelFunc
	lease  *Lease
	err    error
	once   sync.Once
}

func NewExecution(ctx context.Context, host workerproto.MCPHost, stateDir string, definitions []Definition) *Execution {
	ctx, cancel := context.WithCancel(ctx)
	e := &Execution{ready: make(chan struct{}), cancel: cancel}
	if host.Endpoint != "" {
		e.lease, e.err = Acquire(ctx, host, stateDir, definitions)
		close(e.ready)
		return e
	}
	go func() {
		defer close(e.ready)
		if host.Endpoint == "" {
			resolve, ok := ctx.Value(endpointResolverKey{}).(EndpointResolver)
			if !ok {
				e.err = errors.New("MCP host is unavailable")
				return
			}
			waitCtx, stop := context.WithTimeout(ctx, 30*time.Second)
			host, e.err = resolve(waitCtx)
			stop()
			if e.err != nil {
				return
			}
		}
		e.lease, e.err = Acquire(ctx, host, stateDir, definitions)
	}()
	return e
}

func (e *Execution) Context(ctx context.Context) context.Context {
	return mcpclient.WithConnector(ctx, func(ctx context.Context, config mcpclient.Config, search bool) (mcpclient.Connection, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-e.ready:
			if e.err != nil {
				return nil, e.err
			}
			return e.lease.Connect(ctx, config, search)
		}
	})
}

func (e *Execution) Close() error {
	e.once.Do(func() {
		e.cancel()
		<-e.ready
		if e.lease != nil {
			_ = e.lease.Close()
		}
	})
	return nil
}
