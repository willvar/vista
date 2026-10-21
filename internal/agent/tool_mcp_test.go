package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"vista/internal/mcpclient"
	"vista/internal/workerproto"
)

type stubMCPConnection struct {
	closed       atomic.Int32
	closeStarted chan struct{}
	closeRelease chan struct{}
}

func (*stubMCPConnection) ListTools(context.Context, string) (mcpclient.ToolPage, error) {
	return mcpclient.ToolPage{}, nil
}
func (*stubMCPConnection) CallTool(context.Context, string, json.RawMessage) (mcpclient.CallResult, error) {
	return mcpclient.CallResult{}, nil
}
func (s *stubMCPConnection) Close() error {
	s.closed.Add(1)
	if s.closeStarted != nil {
		close(s.closeStarted)
		<-s.closeRelease
	}
	return nil
}

type mcpConnectResult struct {
	server mcpConnection
	err    error
}

func requestMCP(m *mcpManager, ctx context.Context, name string) <-chan mcpConnectResult {
	result := make(chan mcpConnectResult, 1)
	go func() {
		server, err := m.server(ctx, name)
		result <- mcpConnectResult{server, err}
	}()
	return result
}

func receiveMCP[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("MCP operation blocked")
		var zero T
		return zero
	}
}

func TestMCPManagerConnectionsAreIndependentAndShared(t *testing.T) {
	m := newMCPManager([]workerproto.MCPServer{{ID: "ready", Name: "ready"}, {ID: "slow", Name: "slow"}, {ID: "other", Name: "other"}})
	defer m.close()
	slowStarted, release := make(chan struct{}), make(chan struct{})
	ready, slow, other := &stubMCPConnection{}, &stubMCPConnection{}, &stubMCPConnection{}
	var attempts atomic.Int32
	m.connect = func(ctx context.Context, config mcpclient.Config) (mcpConnection, error) {
		switch config.Name {
		case "ready":
			return ready, nil
		case "other":
			return other, nil
		default:
			if attempts.Add(1) == 1 {
				close(slowStarted)
			}
			select {
			case <-release:
				return slow, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	if got := receiveMCP(t, requestMCP(m, context.Background(), "ready")); got.err != nil || got.server != ready {
		t.Fatalf("initial connection: %+v", got)
	}
	initial := requestMCP(m, context.Background(), "slow")
	receiveMCP(t, slowStarted)
	var waiting []<-chan mcpConnectResult
	for i := 0; i < 12; i++ {
		waiting = append(waiting, requestMCP(m, context.Background(), "slow"))
	}
	// A blocked handshake must not block cached connections or another handshake.
	for name, want := range map[string]mcpConnection{"ready": ready, "other": other} {
		if got := receiveMCP(t, requestMCP(m, context.Background(), name)); got.err != nil || got.server != want {
			t.Fatalf("independent %s connection: %+v", name, got)
		}
	}
	// Cancelling a waiter must leave the shared handshake available to others.
	ctx, cancel := context.WithCancel(context.Background())
	queued := requestMCP(m, ctx, "slow")
	cancel()
	if got := receiveMCP(t, queued); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("cancelled waiter: %+v", got)
	}
	close(release)
	for _, ch := range append(waiting, initial) {
		if got := receiveMCP(t, ch); got.err != nil || got.server != slow {
			t.Fatalf("shared connection: %+v", got)
		}
	}
	if attempts.Load() != 1 {
		t.Fatalf("opened the same service %d times", attempts.Load())
	}
	m.close()
	for name, connection := range map[string]*stubMCPConnection{"ready": ready, "slow": slow, "other": other} {
		if connection.closed.Load() != 1 {
			t.Errorf("%s closed %d times", name, connection.closed.Load())
		}
	}
}

func TestMCPManagerUsesStableIDAcrossRenamedExecutionSnapshots(t *testing.T) {
	config := workerproto.MCPServer{ID: "stable-id", Name: "Before rename"}
	for _, label := range []string{"Before rename", "更名后的工具"} {
		config.Name = label
		m := newMCPManager([]workerproto.MCPServer{config})
		defer m.close()
		connection := &stubMCPConnection{}
		calls := 0
		m.connect = func(_ context.Context, got mcpclient.Config) (mcpConnection, error) {
			calls++
			if got.ID != config.ID || got.Name != label {
				t.Fatalf("incorrect connection snapshot: %+v", got)
			}
			return connection, nil
		}
		for i := 0; i < 2; i++ {
			if got, err := m.server(context.Background(), config.ID); err != nil || got != connection {
				t.Fatalf("saved tool call could not resolve stable ID: %v", err)
			}
		}
		if _, err := m.server(context.Background(), label); err == nil || calls != 1 {
			t.Fatalf("display name used as identity or connection reopened: calls=%d err=%v", calls, err)
		}
	}
}

func TestMCPManagerFailedAndCancelledConnectionsCanRetry(t *testing.T) {
	m := newMCPManager([]workerproto.MCPServer{{ID: "service", Name: "service"}})
	defer m.close()
	failure := errors.New("fixture handshake failed")
	started := make(chan struct{})
	connection := &stubMCPConnection{}
	var attempts atomic.Int32
	m.connect = func(ctx context.Context, _ mcpclient.Config) (mcpConnection, error) {
		switch attempts.Add(1) {
		case 1:
			return nil, failure
		case 2:
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		default:
			return connection, nil
		}
	}
	if _, err := m.server(context.Background(), "service"); !errors.Is(err, failure) {
		t.Fatalf("failed connection: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pending := requestMCP(m, ctx, "service")
	receiveMCP(t, started)
	cancel()
	if got := receiveMCP(t, pending); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("cancelled handshake: %+v", got)
	}
	if got := receiveMCP(t, requestMCP(m, context.Background(), "service")); got.err != nil || got.server != connection {
		t.Fatalf("retry after cancellation: %+v", got)
	}
	if attempts.Load() != 3 {
		t.Fatalf("unexpected connection attempts: %d", attempts.Load())
	}
}

func TestMCPManagerCloseCancelsHandshakeAndWaitsForCleanup(t *testing.T) {
	m := newMCPManager([]workerproto.MCPServer{{ID: "service", Name: "service"}})
	started := make(chan struct{})
	connection := &stubMCPConnection{closeStarted: make(chan struct{}), closeRelease: make(chan struct{})}
	m.connect = func(ctx context.Context, _ mcpclient.Config) (mcpConnection, error) {
		close(started)
		<-ctx.Done()
		// Simulate a handshake completing just as the manager closes.
		return connection, nil
	}
	pending := requestMCP(m, context.Background(), "service")
	receiveMCP(t, started)
	closed := make(chan struct{})
	go func() {
		m.close()
		close(closed)
	}()
	receiveMCP(t, connection.closeStarted)
	if got := receiveMCP(t, requestMCP(m, context.Background(), "service")); !errors.Is(got.err, errMCPClosed) {
		t.Fatalf("closed manager accepted a connection: %+v", got)
	}
	select {
	case <-closed:
		t.Fatal("manager closed before pending connection cleanup")
	default:
	}
	close(connection.closeRelease)
	receiveMCP(t, closed)
	if got := receiveMCP(t, pending); !errors.Is(got.err, errMCPClosed) || got.server != nil {
		t.Fatalf("handshake returned a closed connection: %+v", got)
	}
	m.close()
	if connection.closed.Load() != 1 {
		t.Fatalf("connection closed %d times", connection.closed.Load())
	}
}
