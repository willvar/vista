// Package resources stores immutable, content-addressed originals. Database
// owners retain references; files are removed only by explicit storage cleanup.
package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"vista/internal/platform"
)

type Ref struct {
	Hash      string `json:"hash"`
	MediaType string `json:"mediaType"`
	Bytes     int64  `json:"bytes"`
	Name      string `json:"name,omitempty"`
}

type Library struct{ StateDir string }

func ValidHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(hash)
	return err == nil && hex.EncodeToString(decoded) == hash
}

func (l Library) Path(hash string) (string, error) {
	if !ValidHash(hash) {
		return "", errors.New("invalid resource hash")
	}
	return filepath.Join(l.StateDir, "resources", hash[:2], hash), nil
}

// Lock coordinates publishing, reference commits, reads and cleanup across the
// Server and Workers. Callers acquire it before beginning a database transaction.
func (l Library) Lock(ctx context.Context) (func(), error) {
	if err := os.MkdirAll(l.StateDir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(l.StateDir, "resources.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err = ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err = platform.TryLock(f)
		if err == nil {
			return func() { _ = f.Close() }, nil
		}
		if !platform.IsLockBusy(err) {
			f.Close()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// Put streams once into a temporary file while hashing. The caller holds Lock
// until its reference transaction commits. A failed commit leaves an orphan,
// never a reference to a partially written original.
func (l Library) Put(ctx context.Context, input io.Reader) (string, int64, error) {
	dir := filepath.Join(l.StateDir, "resources")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", 0, err
	}
	f, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), input)
	if err != nil {
		return "", 0, err
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	hash := hex.EncodeToString(h.Sum(nil))
	path, _ := l.Path(hash)
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() || info.Size() != n {
			return "", 0, errors.New("resource file is inconsistent")
		}
		return hash, n, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", 0, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", 0, err
	}
	if err := f.Sync(); err != nil {
		return "", 0, err
	}
	if err := f.Close(); err != nil {
		return "", 0, err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return "", 0, err
	}
	return hash, n, nil
}

func (l Library) Read(ref Ref) ([]byte, error) {
	path, err := l.Path(ref.Hash)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || ref.Bytes <= 0 || info.Size() != ref.Bytes {
		return nil, errors.New("resource size does not match its reference")
	}
	data, err := io.ReadAll(io.LimitReader(f, ref.Bytes+1))
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	if int64(len(data)) != ref.Bytes || hex.EncodeToString(hash[:]) != ref.Hash {
		return nil, errors.New("resource content does not match its hash")
	}
	return data, nil
}

// File is either a published original (Hash is set) or an abandoned upload.
// Only Library.Files constructs its removal path.
type File struct {
	Hash  string
	Bytes int64
	path  string
}

func (f File) Remove() error { return os.Remove(f.path) }

func (l Library) Files(ctx context.Context, visit func(File) error) error {
	root := filepath.Join(l.StateDir, "resources")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		hash := entry.Name()
		staged := filepath.Dir(path) == root && strings.HasPrefix(hash, ".upload-")
		if !ValidHash(hash) && !staged {
			return nil
		}
		expected, _ := l.Path(hash)
		if !staged && path != expected || !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if staged {
			hash = ""
		}
		return visit(File{Hash: hash, Bytes: info.Size(), path: path})
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
