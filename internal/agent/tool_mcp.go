package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"vista/internal/mcpclient"
	"vista/internal/provider"
	"vista/internal/workerproto"
)

var mcpListToolsSchema = map[string]any{"type": "function", "function": map[string]any{"name": "mcp_list_tools", "description": "Discover tools on a configured MCP server. Use cursor and offset to continue a long list.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"server": map[string]any{"type": "string", "description": "Stable server id from the configured MCP servers list."}, "cursor": map[string]any{"type": "string"}, "offset": map[string]any{"type": "integer", "minimum": 0}}, "required": []string{"server"}}}}

func (r *toolRuntime) mcpListTools(ctx context.Context, call provider.ToolCall) (string, error) {
	var input struct {
		Server string `json:"server"`
		Cursor string `json:"cursor"`
		Offset int    `json:"offset"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	if input.Offset < 0 || len(input.Cursor) > 512 {
		return "", errors.New("invalid MCP list cursor or offset")
	}
	server, err := r.mcp.server(ctx, input.Server)
	if err != nil {
		return "", err
	}
	page, err := server.ListTools(ctx, input.Cursor)
	if err != nil {
		return "", err
	}
	if input.Offset > len(page.Tools) {
		return "", errors.New("MCP list offset exceeds page")
	}
	selected := []mcpclient.Tool{}
	nextOffset := input.Offset
	for nextOffset < len(page.Tools) {
		candidate := append(selected, page.Tools[nextOffset])
		encoded, _ := json.Marshal(map[string]any{"tools": candidate, "nextOffset": nextOffset + 1, "nextCursor": page.NextCursor})
		if len(encoded) > maxToolResult {
			break
		}
		selected = candidate
		nextOffset++
	}
	if len(selected) == 0 && input.Offset < len(page.Tools) {
		return "", errors.New("MCP tool metadata exceeds model result budget")
	}
	return marshalResult(map[string]any{"tools": selected, "nextOffset": nextOffset, "nextCursor": page.NextCursor, "pageComplete": nextOffset == len(page.Tools)})
}

var mcpCallToolSchema = map[string]any{"type": "function", "function": map[string]any{"name": "mcp_call_tool", "description": "Call one discovered MCP tool with a JSON object of arguments. The call may have side effects and is never replayed automatically. Larger text results become artifacts; use read_tool_output with the returned artifactId to read more.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"server": map[string]any{"type": "string", "description": "Stable server id from the configured MCP servers list."}, "name": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object"}}, "required": []string{"server", "name", "arguments"}}}}

func (r *toolRuntime) mcpCallTool(ctx context.Context, call provider.ToolCall) (string, error) {
	var input struct {
		Server    string          `json:"server"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	server, err := r.mcp.server(ctx, input.Server)
	if err != nil {
		return "", err
	}
	result, err := server.CallTool(ctx, input.Name, input.Arguments)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	if result.IsError {
		return string(encoded), errors.New("MCP tool reported an error")
	}
	return string(encoded), nil
}

type mcpManager struct {
	mu        sync.Mutex
	configs   map[string]workerproto.MCPServer
	open      map[string]*mcpEntry
	connect   func(context.Context, mcpclient.Config) (mcpConnection, error)
	closed    bool
	closeOnce sync.Once
}

type mcpConnection interface {
	ListTools(context.Context, string) (mcpclient.ToolPage, error)
	CallTool(context.Context, string, json.RawMessage) (mcpclient.CallResult, error)
	Close() error
}

type mcpEntry struct {
	ready  chan struct{}
	cancel context.CancelFunc
	server mcpConnection
	err    error
}

var errMCPClosed = errors.New("MCP connections are closed")

func newMCPManager(configs []workerproto.MCPServer) *mcpManager {
	m := &mcpManager{
		configs: map[string]workerproto.MCPServer{}, open: map[string]*mcpEntry{},
		connect: func(ctx context.Context, config mcpclient.Config) (mcpConnection, error) {
			return mcpclient.Open(ctx, config)
		},
	}
	for _, config := range configs {
		m.configs[config.ID] = config
	}
	return m
}
func (m *mcpManager) server(ctx context.Context, id string) (mcpConnection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errMCPClosed
	}
	if entry := m.open[id]; entry != nil {
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-entry.ready:
			return entry.server, entry.err
		}
	}
	config, ok := m.configs[id]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("MCP server %q is not configured", id)
	}
	connectCtx, cancel := context.WithCancel(ctx)
	entry := &mcpEntry{ready: make(chan struct{}), cancel: cancel}
	m.open[id] = entry
	m.mu.Unlock()

	// Only callers for this ID wait for the handshake. Other established or
	// connecting servers remain accessible while this connection is pending.
	server, err := m.connect(connectCtx, config)
	cancel()
	m.mu.Lock()
	if m.closed {
		err = errMCPClosed
	} else if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil && server != nil {
		// Keep the pending entry visible until cleanup finishes: close waits for
		// it, and callers for the same ID cannot start a competing connection yet.
		m.mu.Unlock()
		_ = server.Close()
		server = nil
		m.mu.Lock()
	}
	entry.server, entry.err = server, err
	if err != nil {
		delete(m.open, id)
	}
	close(entry.ready)
	m.mu.Unlock()
	return server, err
}

func (m *mcpManager) close() {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		entries := make([]*mcpEntry, 0, len(m.open))
		for _, entry := range m.open {
			entry.cancel()
			entries = append(entries, entry)
		}
		m.mu.Unlock()
		for _, entry := range entries {
			<-entry.ready
			if entry.server != nil {
				_ = entry.server.Close()
			}
		}
	})
}
