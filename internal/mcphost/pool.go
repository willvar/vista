package mcphost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"vista/internal/mcpclient"
)

type entry struct {
	definition     Definition
	key            string
	baseKey        string
	current        bool
	refs           int
	ctx            context.Context
	cancel         context.CancelFunc
	authDir        string
	ready          chan struct{}
	connecting     bool
	server         mcpclient.Connection
	err            error
	retryAt        time.Time
	connectedAt    time.Time
	reconnects     int
	reconnectDelay time.Duration
	state          State
}

// Pool retains current versions and execution leases. Its context, rather than
// a caller's context, owns handshakes and connections shared by other callers.
type Pool struct {
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	configDir string
	directory string
	entries   map[string]*entry
	revision  uint64
	sequence  uint64
	closed    bool
	wg        sync.WaitGroup
	slots     chan struct{}
	notify    func(Event)
	connect   func(context.Context, Definition, string) (mcpclient.Connection, error)
}

func NewPool(ctx context.Context, configDir, directory string, notify func(Event)) *Pool {
	ctx, cancel := context.WithCancel(ctx)
	p := &Pool{ctx: ctx, cancel: cancel, configDir: configDir, directory: directory, entries: map[string]*entry{}, slots: make(chan struct{}, 3), notify: notify}
	p.connect = func(ctx context.Context, def Definition, authDir string) (mcpclient.Connection, error) {
		if def.Search {
			return mcpclient.OpenSearch(ctx, def.Config.Endpoint, def.Config.Token)
		}
		return mcpclient.OpenWithConfigDir(ctx, def.Config, authDir)
	}
	return p
}

func validate(definitions []Definition) error {
	if len(definitions) > 16 {
		return errors.New("too many MCP connection definitions")
	}
	for _, def := range definitions {
		if err := mcpclient.ValidateConfig(def.Config); err != nil {
			return err
		}
		if def.Search && (def.Config.Transport != "http" || def.Config.OAuth) {
			return errors.New("invalid MCP search connection")
		}
	}
	return nil
}

// Caller holds mu. No transport operations take place under this lock.
func (p *Pool) entryLocked(def Definition) (*entry, error) {
	baseKey := Key(def)
	authKey, snapshot := mcpclient.OAuthSnapshot(p.configDir, def.Config)
	key := baseKey + authKey
	if e := p.entries[key]; e != nil {
		return e, nil
	}
	authDir := filepath.Join(p.directory, "auth", key+"-"+uuid.NewString())
	if snapshot != nil {
		if err := snapshot(authDir); err != nil {
			_ = os.RemoveAll(authDir)
			return nil, fmt.Errorf("snapshot MCP authorization: %w", err)
		}
	}
	ctx, cancel := context.WithCancel(p.ctx)
	e := &entry{definition: def, key: key, baseKey: baseKey, ctx: ctx, cancel: cancel, authDir: authDir}
	p.entries[key] = e
	return e, nil
}

// Preparing a snapshot starts no connections. Roll back unpublished entries if
// any authorization copy fails, without disturbing current or leased versions.
func (p *Pool) prepareLocked(definitions []Definition) (map[string]*entry, error) {
	entries := make(map[string]*entry, len(definitions))
	for _, def := range definitions {
		e, err := p.entryLocked(def)
		if err != nil {
			for _, prepared := range entries {
				if !prepared.current && prepared.refs == 0 {
					p.retireLocked(prepared)
				}
			}
			return nil, err
		}
		entries[e.baseKey] = e
	}
	return entries, nil
}

func (p *Pool) Sync(revision uint64, definitions []Definition) error {
	if err := validate(definitions); err != nil {
		return err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("MCP host is stopping")
	}
	if revision < p.revision {
		p.mu.Unlock()
		return nil
	}
	current, err := p.prepareLocked(definitions)
	if err != nil {
		p.mu.Unlock()
		return err
	}
	p.revision = revision
	for _, e := range p.entries {
		e.current = current[e.baseKey] == e
		if !e.current && e.refs == 0 {
			p.retireLocked(e)
		} else if e.current && e.server == nil && !e.connecting {
			p.startLocked(e)
		}
	}
	p.mu.Unlock()
	for _, e := range current {
		p.publish(e)
	}
	return nil
}

func (p *Pool) retain(definitions []Definition) (map[string]*entry, func(), error) {
	if err := validate(definitions); err != nil {
		return nil, nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, nil, errors.New("MCP host is stopping")
	}
	entries, err := p.prepareLocked(definitions)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		e.refs++
		if e.server == nil && !e.connecting && (e.ready == nil || time.Now().After(e.retryAt)) {
			p.startLocked(e)
		}
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			for _, e := range entries {
				e.refs--
				if e.refs == 0 && !e.current {
					p.retireLocked(e)
				}
			}
		})
	}
	return entries, release, nil
}

func (p *Pool) startLocked(e *entry) {
	if p.closed || e.ctx.Err() != nil {
		return
	}
	e.connecting, e.ready = true, make(chan struct{})
	e.state = State{Status: "connecting"}
	p.wg.Go(func() { p.initialize(e) })
}

func (p *Pool) initialize(e *entry) {
	var server mcpclient.Connection
	var err error
	if e.reconnectDelay > 0 {
		timer := time.NewTimer(e.reconnectDelay)
		select {
		case <-e.ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(1<<(attempt-1)) * time.Second)
			select {
			case <-e.ctx.Done():
				timer.Stop()
				err = e.ctx.Err()
			case <-timer.C:
			}
		}
		if e.ctx.Err() != nil {
			err = e.ctx.Err()
			break
		}
		p.publish(e)
		select {
		case p.slots <- struct{}{}:
			server, err = p.connect(e.ctx, e.definition, e.authDir)
			<-p.slots
		case <-e.ctx.Done():
			err = e.ctx.Err()
		}
		if err == nil || e.definition.Config.OAuth && !mcpclient.OAuthConfigured(e.authDir, e.definition.Config) {
			break
		}
	}
	p.mu.Lock()
	if e.ctx.Err() != nil {
		err = e.ctx.Err()
	}
	e.server, e.err, e.connecting = server, err, false
	e.connectedAt = time.Now()
	e.retryAt = time.Now().Add(30 * time.Second)
	e.state = State{Status: "available", CheckedAt: time.Now().UTC(), ToolCount: -1}
	if err != nil {
		e.server = nil
		e.state.Status, e.state.Error = "error", mcpclient.RedactError(err.Error(), e.definition.Config)
		if e.definition.Config.OAuth && !mcpclient.OAuthConfigured(e.authDir, e.definition.Config) {
			e.state.Status = "authorization_required"
		}
	}
	close(e.ready)
	if err == nil {
		if waiter, ok := server.(interface{ Wait() error }); ok {
			p.wg.Go(func() { p.watch(e, server, waiter.Wait) })
		}
	}
	p.mu.Unlock()
	if err != nil && server != nil {
		_ = server.Close()
	}
	p.publish(e)
}

func (p *Pool) watch(e *entry, server mcpclient.Connection, wait func() error) {
	err := wait()
	if err == nil {
		err = errors.New("MCP service disconnected")
	}
	p.mu.Lock()
	if e.server != server || e.ctx.Err() != nil || p.closed {
		p.mu.Unlock()
		return
	}
	e.server, e.err = nil, err
	if time.Since(e.connectedAt) > time.Minute {
		e.reconnects = 0
	}
	e.reconnects++
	e.retryAt = time.Now().Add(30 * time.Second)
	e.state = State{Status: "error", Error: mcpclient.RedactError(err.Error(), e.definition.Config), CheckedAt: time.Now().UTC()}
	p.mu.Unlock()
	p.publish(e)
	_ = server.Close()
	p.mu.Lock()
	if !e.connecting && e.reconnects <= 3 {
		e.reconnectDelay = time.Duration(1<<(e.reconnects-1)) * time.Second
		p.startLocked(e)
	}
	p.mu.Unlock()
}

func (p *Pool) get(ctx context.Context, e *entry, force bool) (mcpclient.Connection, error) {
	p.mu.Lock()
	if e.ctx.Err() != nil || p.closed {
		p.mu.Unlock()
		return nil, errors.New("MCP connection was released")
	}
	if e.server == nil && !e.connecting && (force || time.Now().After(e.retryAt)) {
		e.reconnects, e.reconnectDelay = 0, 0
		p.startLocked(e)
	}
	ready := e.ready
	p.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.ctx.Done():
		return nil, e.ctx.Err()
	case <-ready:
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return e.server, e.err
}

func (p *Pool) publish(e *entry) {
	p.mu.Lock()
	if !e.current || p.closed || e.definition.Search {
		p.mu.Unlock()
		return
	}
	p.sequence++
	event := Event{Kind: "state", Sequence: p.sequence, Revision: p.revision, Key: e.baseKey, ID: e.definition.Config.ID, State: e.state}
	p.mu.Unlock()
	if p.notify != nil {
		p.notify(event)
	}
}

func (p *Pool) retireLocked(e *entry) {
	if p.entries[e.key] != e {
		return
	}
	delete(p.entries, e.key)
	e.cancel()
	ready := e.ready
	p.wg.Go(func() {
		if ready != nil {
			<-ready
		}
		p.mu.Lock()
		server := e.server
		e.server = nil
		p.mu.Unlock()
		if server != nil {
			_ = server.Close()
		}
		_ = os.RemoveAll(e.authDir)
	})
}

func (p *Pool) Close() {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		p.cancel()
		for _, e := range p.entries {
			p.retireLocked(e)
		}
	}
	p.mu.Unlock()
	p.wg.Wait()
}
