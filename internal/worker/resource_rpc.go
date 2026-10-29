package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"vista/internal/platform"

	"vista/internal/instance"
	"vista/internal/resources"
	"vista/internal/workerproto"
)

var workerMemoryLimit atomic.Uint64

func importResource(ctx context.Context, command workerproto.Command, emit func(context.Context, workerproto.Result) error) (*resources.Ref, error) {
	if err := emit(ctx, workerproto.Result{Kind: "media_admission", RunID: command.RunID, Metrics: &workerproto.Metrics{RSSBytes: platform.RSS()}}); err != nil {
		return nil, err
	}
	dir, err := instance.Dir()
	if err != nil {
		return nil, err
	}
	// HTTP uploads must be the Server's private staged files. Local tool imports
	// have their own explicit path argument and do not pass through this command.
	relative, err := filepath.Rel(filepath.Join(dir, "inputs"), command.ResourcePath)
	if err != nil || filepath.IsAbs(relative) || strings.HasPrefix(relative, "..") || filepath.Dir(relative) != "." {
		return nil, errors.New("invalid staged image path")
	}
	defer os.Remove(command.ResourcePath)
	library := resources.Library{StateDir: dir}
	unlock, err := library.Lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	ref, err := library.ImportImage(ctx, command.ResourcePath, command.ResourceName, command.ResourceType)
	if err != nil {
		return nil, err
	}
	if err = emit(ctx, workerproto.Result{Kind: "resource_import", RPCID: command.RPCID, RunID: command.RunID, Resource: &ref}); err != nil {
		return nil, err
	}
	return &ref, nil
}
func setMemoryLimit(limit int64) {
	if limit > 0 {
		workerMemoryLimit.Store(uint64(limit))
		debug.SetMemoryLimit(limit * 3 / 4)
	}
}
