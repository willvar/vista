package mcphost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"vista/internal/mcpclient"
	"vista/internal/platform"
)

func TestMain(m *testing.M) {
	if os.Getenv("VISTA_MCP_HOST_PROCESS") == "1" {
		if err := Run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type identity struct {
	PID        int `json:"pid"`
	Descendant int `json:"descendant"`
	Calls      int `json:"calls"`
}

func TestHostToolFixture(t *testing.T) {
	if os.Getenv("VISTA_HOST_TOOL_FIXTURE") != "1" {
		return
	}
	var count atomic.Int32
	var once sync.Once
	var descendant int
	server := mcp.NewServer(&mcp.Implementation{Name: "host-fixture", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "identity", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		once.Do(func() {
			child := exec.Command(os.Args[0], "-test.run=^TestHostDescendantFixture$")
			if child.Start() == nil {
				descendant = child.Process.Pid
				go child.Wait()
			}
		})
		encoded, _ := json.Marshal(identity{PID: os.Getpid(), Descendant: descendant, Calls: int(count.Add(1))})
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil
	})
	server.AddTool(&mcp.Tool{Name: "wait", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		file, err := os.OpenFile("called", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err == nil {
			_, _ = file.WriteString("once\n")
			_ = file.Close()
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestHostDescendantFixture(t *testing.T) {
	if os.Getenv("VISTA_HOST_TOOL_FIXTURE") == "1" {
		time.Sleep(time.Hour)
	}
}

func TestProcessRestartCleansDescendantsAndNeverReplaysCalls(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess lifecycle")
	}
	t.Setenv("VISTA_HOST_TOOL_FIXTURE", "1")
	configDir, stateDir, workDir := t.TempDir(), t.TempDir(), t.TempDir()
	def := Definition{Config: mcpclient.Config{ID: "local", Name: "local", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestHostToolFixture$"}, Directory: workDir}}
	ctx, cancel := context.WithCancel(context.Background())
	var available atomic.Int32
	process := NewProcess(ProcessOptions{
		ConfigDir: configDir, StateDir: stateDir, Env: os.Environ(),
		Snapshot: func() ([]Definition, int64) { return []Definition{def}, 0 },
		State: func(event Event) {
			if event.State.Status == "available" {
				available.Add(1)
			}
		},
	})
	stopped := make(chan struct{})
	go func() { defer close(stopped); process.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(15 * time.Second):
			t.Error("host supervisor did not stop")
		}
	})
	deadline, stop := context.WithTimeout(ctx, 12*time.Second)
	defer stop()
	host, err := process.Endpoint(deadline)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Acquire(deadline, host, stateDir, []Definition{def})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	proxy, err := first.Connect(ctx, def.Config, false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := proxy.CallTool(ctx, "identity", json.RawMessage(`{}`))
	var original identity
	if err != nil || json.Unmarshal([]byte(result.Content), &original) != nil || original.Descendant == 0 {
		t.Fatalf("fixture identity: %s %v", result.Content, err)
	}
	second, err := Acquire(deadline, host, stateDir, []Definition{def})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	shared, _ := second.Connect(ctx, def.Config, false)
	result, err = shared.CallTool(ctx, "identity", json.RawMessage(`{}`))
	var same identity
	if err != nil || json.Unmarshal([]byte(result.Content), &same) != nil || same.PID != original.PID || same.Calls != 2 {
		t.Fatalf("local process was not reused: %s %v", result.Content, err)
	}
	// Killing only the local server reconnects its version while preserving the
	// host and existing leases. No caller must repeat initialize manually.
	before := available.Load()
	local, _ := os.FindProcess(original.PID)
	if err := local.Kill(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return !platform.IsAlive(original.PID) })
	eventually(t, func() bool { return available.Load() > before })
	result, err = shared.CallTool(ctx, "identity", json.RawMessage(`{}`))
	var reconnected identity
	if err != nil || json.Unmarshal([]byte(result.Content), &reconnected) != nil || reconnected.PID == original.PID {
		t.Fatalf("local connection did not recover for its existing lease: %s %v", result.Content, err)
	}
	// The disconnected server's descendants are also part of the host session
	// and are cleaned up when that host exits below.
	interrupted := make(chan error, 1)
	go func() { _, err := shared.CallTool(ctx, "wait", json.RawMessage(`{}`)); interrupted <- err }()
	eventually(t, func() bool { data, _ := os.ReadFile(filepath.Join(workDir, "called")); return string(data) == "once\n" })
	oldPID, _ := process.Status()
	child, _ := os.FindProcess(oldPID)
	if err := child.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-interrupted:
		if err == nil {
			t.Fatal("host crash reported a successful tool call")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("host crash did not interrupt the tool")
	}
	eventually(t, func() bool { pid, _ := process.Status(); return pid != 0 && pid != oldPID })
	eventually(t, func() bool {
		return !platform.IsAlive(original.PID) && !platform.IsAlive(original.Descendant) && !platform.IsAlive(reconnected.PID) && !platform.IsAlive(reconnected.Descendant)
	})
	hostAfter, err := process.Endpoint(deadline)
	if err != nil || hostAfter != host {
		t.Fatalf("restart changed the private endpoint: %+v %v", hostAfter, err)
	}
	third, err := Acquire(deadline, hostAfter, stateDir, []Definition{def})
	if err != nil {
		t.Fatal(err)
	}
	newProxy, _ := third.Connect(ctx, def.Config, false)
	result, err = newProxy.CallTool(ctx, "identity", json.RawMessage(`{}`))
	var replacement identity
	if err != nil || json.Unmarshal([]byte(result.Content), &replacement) != nil || replacement.PID == original.PID {
		t.Fatalf("connection did not recover: %s %v", result.Content, err)
	}
	if _, err := shared.CallTool(ctx, "identity", json.RawMessage(`{}`)); err == nil {
		t.Fatal("old execution lease survived host replacement")
	}
	_ = third.Close()
	cancel()
	select {
	case <-stopped:
	case <-time.After(12 * time.Second):
		t.Fatal("supervisor did not stop")
	}
	eventually(t, func() bool { return !platform.IsAlive(replacement.PID) && !platform.IsAlive(replacement.Descendant) })
	data, _ := os.ReadFile(filepath.Join(workDir, "called"))
	if strings.Count(string(data), "once") != 1 {
		t.Fatalf("interrupted call was replayed: %q", data)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "mcp-host.json")); !os.IsNotExist(err) {
		t.Fatalf("ownership record survived clean stop: %v", err)
	}
}
