package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"vista/internal/platform"
)

// Canonicalize through the nearest existing ancestor, including paths whose
// parents do not exist yet. Locks cover the same physical files across Workers.
func canonicalWritePath(path string) (string, error) {
	path, err := absolute(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if filepath.Dir(path) == path {
		return "", err
	}
	parent, err := canonicalWritePath(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

func lockFilePaths(ctx context.Context, stateDir string, paths []string) (func(), error) {
	if stateDir == "" {
		return nil, errors.New("state directory is required")
	}
	directory := filepath.Join(stateDir, "file-locks")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	paths = slices.Clone(paths)
	slices.Sort(paths)
	paths = slices.Compact(paths)
	var files []*os.File
	release := func() {
		for i := len(files) - 1; i >= 0; i-- {
			_ = files[i].Close()
		}
	}
	for _, path := range paths {
		digest := sha256.Sum256([]byte(path))
		file, err := os.OpenFile(filepath.Join(directory, hex.EncodeToString(digest[:])+".lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			release()
			return nil, err
		}
		files = append(files, file)
		for {
			if err := ctx.Err(); err != nil {
				release()
				return nil, err
			}
			if err := platform.TryLock(file); err == nil {
				break
			} else if !platform.IsLockBusy(err) {
				release()
				return nil, err
			}
			timer := time.NewTimer(20 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				release()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return release, nil
}
