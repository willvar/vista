package mcphost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"vista/internal/platform"
	"vista/internal/workerproto"
)

// Run is the hidden MCP-host subprocess entry point. EOF is its ownership
// channel on every platform; Unix signals and Windows Jobs cover parent death.
func Run() error {
	_ = os.Unsetenv("VISTA_MCP_HOST_PROCESS")
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()
	controls := make(chan control)
	go func() {
		defer close(controls)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 4096), maxControlBytes)
		for scanner.Scan() {
			var next control
			if json.Unmarshal(scanner.Bytes(), &next) != nil {
				return
			}
			select {
			case controls <- next:
			case <-ctx.Done():
				return
			}
		}
	}()
	var boot control
	select {
	case initial, ok := <-controls:
		if !ok {
			return errors.New("MCP host startup channel closed")
		}
		boot = initial
	case <-ctx.Done():
		return ctx.Err()
	}
	if len(boot.Token) != 64 || !filepath.IsAbs(boot.ConfigDir) || !filepath.IsAbs(boot.StateDir) {
		return errors.New("invalid MCP host startup")
	}
	address := "127.0.0.1:0"
	if boot.Endpoint != "" {
		address = strings.TrimPrefix(boot.Endpoint, "http://")
		if !strings.HasPrefix(address, "127.0.0.1:") {
			return errors.New("invalid MCP host address")
		}
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	root := filepath.Join(boot.StateDir, "mcp-host-data")
	directory := filepath.Join(root, strconv.Itoa(os.Getpid()))
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	defer platform.KillSessionDescendants(os.Getpid())
	var outputMu sync.Mutex
	encoder := json.NewEncoder(os.Stdout)
	emit := func(event Event) {
		outputMu.Lock()
		defer outputMu.Unlock()
		if encoder.Encode(event) != nil {
			cancel()
		}
	}
	pool := NewPool(ctx, boot.ConfigDir, directory, emit)
	defer pool.Close()
	handler := &hostHandler{pool: pool, token: boot.Token, root: root, leases: map[string]*hostedLease{}}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 8192, BaseContext: func(net.Listener) context.Context { return ctx }}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	defer func() {
		cancel()
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if server.Shutdown(shutdown) != nil {
			_ = server.Close()
		}
	}()
	var memoryLimit atomic.Int64
	setLimit := func(limit int64) {
		memoryLimit.Store(limit)
		if limit > 0 {
			debug.SetMemoryLimit(limit * 9 / 10)
		}
	}
	setLimit(boot.MemoryLimit)
	metricsDone := make(chan struct{})
	defer close(metricsDone)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			var stats runtime.MemStats
			runtime.ReadMemStats(&stats)
			rss := platform.RSS()
			emit(Event{Kind: "metrics", Metrics: &workerproto.Metrics{HeapBytes: stats.HeapAlloc, RSSBytes: rss, PeakRSSBytes: platform.PeakRSS(), SysBytes: stats.Sys, TotalAllocBytes: stats.TotalAlloc, Goroutines: runtime.NumGoroutine(), SampledAt: time.Now().UTC().Format(time.RFC3339Nano)}})
			if limit := memoryLimit.Load(); limit > 0 && rss > uint64(limit) {
				cancel()
				return
			}
			select {
			case <-metricsDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	emit(Event{Kind: "ready", Endpoint: "http://" + listener.Addr().String()})
	if err := pool.Sync(boot.Revision, boot.Definitions); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-served:
			return fmt.Errorf("MCP host listener: %w", err)
		case next, ok := <-controls:
			if !ok {
				return nil
			}
			setLimit(next.MemoryLimit)
			if err := pool.Sync(next.Revision, next.Definitions); err != nil {
				return err
			}
		}
	}
}
