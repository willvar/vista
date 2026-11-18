package resources

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/google/uuid"
	"vista/internal/platform"
)

// Verification is not a resource publication. Its backend-local lock excludes
// reclamation without blocking conversations using the active backend.
func (l Library) openCheckLock(id string) (*os.File, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, errors.New("invalid storage identity")
	}
	dir := filepath.Join(l.StateDir, "resource-backends", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(dir, "check.lock"), os.O_CREATE|os.O_RDWR, 0600)
}
func (l Library) lockBackendCheck(ctx context.Context, id string) (func(), error) {
	file, err := l.openCheckLock(id)
	if err != nil {
		return nil, err
	}
	for {
		if err = ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		if err = platform.TryLock(file); err == nil {
			return func() { _ = file.Close() }, nil
		} else if !platform.IsLockBusy(err) {
			file.Close()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
func (l Library) BackendBusy(id string) (bool, error) {
	if id == LocalBackend {
		return false, nil
	}
	file, err := l.openCheckLock(id)
	if err != nil {
		return false, err
	}
	defer file.Close()
	err = platform.TryLock(file)
	if platform.IsLockBusy(err) {
		return true, nil
	}
	return false, err
}

func syncResourceDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
