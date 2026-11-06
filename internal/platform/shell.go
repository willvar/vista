package platform

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// ShellScriptDirectory belongs to one Worker. Its supervisor can remove it
// after the process exits, including when the Worker could not run defers.
func ShellScriptDirectory(stateDir string, pid int) string {
	return filepath.Join(stateDir, "shell-scripts", strconv.Itoa(pid))
}

// ShellCommand keeps command text out of the OS command line. The caller owns
// cleanup until the command and its managed descendants have finished.
func ShellCommand(ctx context.Context, stateDir, script string) (*exec.Cmd, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	directory := ShellScriptDirectory(stateDir, os.Getpid())
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, nil, err
	}
	extension, content := shellScript(script)
	file, err := os.CreateTemp(directory, "command-*"+extension)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.Remove(file.Name()) }
	if _, err = file.WriteString(content); err != nil {
		_ = file.Close()
		cleanup()
		return nil, nil, err
	}
	if err = file.Close(); err != nil {
		cleanup()
		return nil, nil, err
	}
	return shellFileCommand(ctx, file.Name()), cleanup, nil
}
