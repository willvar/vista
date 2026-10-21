package mcpclient

import (
	"context"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"vista/internal/platform"
)

// Keep SDK framing while giving the subprocess the same ownership and tree
// cleanup as shell tools. SDK CommandTransport starts and kills only its root.
type commandTransport struct {
	command *exec.Cmd
	conn    *commandConnection
}

func (t *commandTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	stdout, err := t.command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := t.command.StdinPipe()
	if err != nil {
		stdout.Close()
		return nil, err
	}
	platform.PrepareCommand(t.command)
	if err := platform.Start(t.command); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, err
	}
	conn, err := (&mcp.IOTransport{Reader: io.NopCloser(stdout), Writer: stdin}).Connect(ctx)
	if err != nil {
		_ = platform.KillProcessGroup(t.command.Process)
		_ = t.command.Wait()
		stdin.Close()
		stdout.Close()
		return nil, err
	}
	t.conn = &commandConnection{Connection: conn, command: t.command, stdin: stdin, stdout: stdout}
	return t.conn, nil
}

type commandConnection struct {
	mcp.Connection
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	once    sync.Once
	err     error
}

func (c *commandConnection) Close() error {
	c.once.Do(func() {
		_ = c.stdin.Close()
		done := make(chan error, 1)
		go func() { done <- c.command.Wait() }()
		select {
		case c.err = <-done:
		case <-time.After(2 * time.Second):
			_ = platform.KillProcessGroup(c.command.Process)
			c.err = <-done
		}
		_ = platform.KillProcessGroup(c.command.Process)
		_ = c.stdout.Close()
		_ = c.Connection.Close()
	})
	return c.err
}
