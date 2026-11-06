//go:build !windows

package platform

import (
	"context"
	"os/exec"
)

func shellScript(script string) (string, string) { return ".sh", script }

func shellFileCommand(ctx context.Context, path string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/bin/sh", path)
	PrepareCommand(cmd)
	return cmd
}
