package mcphost

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"vista/internal/platform"
	"vista/internal/workerproto"
)

type ProcessOptions struct {
	ConfigDir string
	StateDir  string
	Env       []string
	Snapshot  func() ([]Definition, int64)
	State     func(Event)
	Stopped   func()
}

type Process struct {
	mu       sync.Mutex
	options  ProcessOptions
	host     workerproto.MCPHost
	ready    bool
	changed  chan struct{}
	wake     chan struct{}
	err      error
	pid      int
	revision uint64
	metrics  workerproto.Metrics
	states   map[string]uint64
}

func NewProcess(options ProcessOptions) *Process {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		panic(err)
	}
	return &Process{options: options, host: workerproto.MCPHost{Managed: true, Token: hex.EncodeToString(token[:])}, changed: make(chan struct{}), wake: make(chan struct{}, 1), states: map[string]uint64{}}
}

func (p *Process) Descriptor() workerproto.MCPHost {
	p.mu.Lock()
	defer p.mu.Unlock()
	host := p.host
	if !p.ready {
		host.Endpoint = ""
	}
	return host
}

// Refresh coalesces changes; the supervisor reads the latest committed settings
// after taking the wakeup, so delayed writers cannot resend an older snapshot.
func (p *Process) Refresh() {
	p.mu.Lock()
	p.revision++
	if !p.ready {
		p.err = nil
	}
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *Process) Endpoint(ctx context.Context) (workerproto.MCPHost, error) {
	for {
		p.mu.Lock()
		ready, changed, host, err := p.ready, p.changed, p.host, p.err
		p.mu.Unlock()
		if ready {
			return host, nil
		}
		if err != nil {
			return workerproto.MCPHost{}, err
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return workerproto.MCPHost{}, ctx.Err()
		}
	}
}

func (p *Process) Status() (int, workerproto.Metrics) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pid, p.metrics
}

func (p *Process) changedLocked() {
	close(p.changed)
	p.changed = make(chan struct{})
}

func (p *Process) configuration() control {
	p.mu.Lock()
	revision, host := p.revision, p.host
	p.mu.Unlock()
	definitions, limit := p.options.Snapshot()
	return control{Endpoint: host.Endpoint, Token: host.Token, ConfigDir: p.options.ConfigDir, StateDir: p.options.StateDir, Definitions: definitions, Revision: revision, MemoryLimit: limit}
}

func (p *Process) Run(ctx context.Context) {
	defer func() {
		p.mu.Lock()
		p.ready, p.err = false, errors.New("MCP host stopped")
		p.changedLocked()
		p.mu.Unlock()
	}()
	failures := 0
	for ctx.Err() == nil {
		started := time.Now()
		err := p.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			failures = 0
		}
		failures++
		log.Printf("MCP host interrupted: %v", err)
		if failures >= 3 {
			p.mu.Lock()
			p.err = errors.New("MCP host could not restart; retry a connection check or save MCP settings")
			p.changedLocked()
			p.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-p.wake:
				failures = 0
			}
		} else {
			timer := time.NewTimer(time.Duration(1<<(failures-1)) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

func (p *Process) runOnce(ctx context.Context) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "mcp-host")
	cmd.Env = append(append([]string(nil), p.options.Env...), "VISTA_MCP_HOST_PROCESS=1")
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	defer output.Close()
	logFile, err := os.OpenFile(filepath.Join(p.options.StateDir, "mcp.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd.Stderr = logFile
	platform.AttachToParent(cmd)
	if err := platform.Start(cmd); err != nil {
		return err
	}
	p.mu.Lock()
	p.pid, p.ready, p.err = cmd.Process.Pid, false, nil
	p.revision++
	p.states = map[string]uint64{}
	p.changedLocked()
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.ready = false
		p.changedLocked()
		p.mu.Unlock()
		_ = input.Close()
		// The host first receives EOF and closes its servers. Its process remains
		// unreaped until platform cleanup has checked its descendants.
		timeout := time.AfterFunc(5*time.Second, func() { _ = cmd.Process.Kill() })
		_, _ = io.Copy(io.Discard, output)
		timeout.Stop()
		_ = cmd.Process.Kill()
		if err := platform.KillSessionDescendants(cmd.Process.Pid); err != nil {
			log.Printf("MCP host descendant cleanup: %v", err)
		} else {
			_ = os.Remove(filepath.Join(p.options.StateDir, "mcp-host.json"))
		}
		_ = cmd.Wait()
		if err := os.RemoveAll(filepath.Join(p.options.StateDir, "mcp-host-data", strconv.Itoa(cmd.Process.Pid))); err != nil {
			log.Printf("MCP host temporary output cleanup: %v", err)
		}
		p.mu.Lock()
		p.pid, p.ready, p.metrics = 0, false, workerproto.Metrics{}
		p.changedLocked()
		p.mu.Unlock()
		if p.options.Stopped != nil {
			p.options.Stopped()
		}
	}()
	start, err := platform.WorkerSessionStart(cmd.Process.Pid)
	if err != nil {
		return err
	}
	if err := saveOwner(p.options.StateDir, owner{PID: cmd.Process.Pid, StartTime: start}); err != nil {
		return err
	}
	encoder := json.NewEncoder(input)
	if err := encoder.Encode(p.configuration()); err != nil {
		return err
	}
	events := make(chan Event)
	readerDone := make(chan struct{})
	defer close(readerDone)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), maxControlBytes)
		for scanner.Scan() {
			var event Event
			if json.Unmarshal(scanner.Bytes(), &event) != nil {
				return
			}
			select {
			case events <- event:
			case <-readerDone:
				return
			}
		}
	}()
	readyTimeout := time.NewTimer(5 * time.Second)
	defer readyTimeout.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-readyTimeout.C:
			return errors.New("MCP host readiness timeout")
		case <-p.wake:
			if err := encoder.Encode(p.configuration()); err != nil {
				return err
			}
		case event, ok := <-events:
			if !ok {
				return errors.New("MCP host output closed")
			}
			p.mu.Lock()
			switch event.Kind {
			case "ready":
				p.host.Endpoint, p.ready = event.Endpoint, true
				readyTimeout.Stop()
				p.changedLocked()
			case "metrics":
				if event.Metrics != nil {
					p.metrics = *event.Metrics
				}
			}
			current := event.Revision == p.revision && event.Sequence > p.states[event.Key]
			if current && event.Kind == "state" {
				p.states[event.Key] = event.Sequence
			}
			p.mu.Unlock()
			if event.Kind == "state" && current && p.options.State != nil {
				p.options.State(event)
			}
		}
	}
}

type owner struct {
	PID       int    `json:"pid"`
	StartTime uint64 `json:"startTime"`
}

func saveOwner(dir string, value owner) error {
	file, err := os.CreateTemp(dir, ".mcp-owner-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	writeErr := json.NewEncoder(file).Encode(value)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, "mcp-host.json"))
}

// Recover runs after old Workers are gone and before a new host can launch.
func Recover(dir string) error {
	path := filepath.Join(dir, "mcp-host.json")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		var previous owner
		if len(data) > 256 || json.Unmarshal(data, &previous) != nil {
			return errors.New("invalid MCP host ownership record")
		}
		if err := platform.KillOwnedSession(previous.PID, previous.StartTime); err != nil && !errors.Is(err, platform.ErrIdentityMismatch) {
			return fmt.Errorf("clean up previous MCP host: %w", err)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return os.RemoveAll(filepath.Join(dir, "mcp-host-data"))
}
