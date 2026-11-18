package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Original owns a private, bounded-buffer materialization when a remote
// resource needs seeking. Close removes that copy on every supported platform.
type Original struct {
	*os.File
	temporary string
}

func (f *Original) Close() error {
	err := f.File.Close()
	if f.temporary != "" {
		err = errors.Join(err, os.Remove(f.temporary))
	}
	return err
}

func (l Library) Put(ctx context.Context, input io.Reader) (string, int64, error) {
	stamp, err := l.Stamp(ctx)
	if err != nil {
		return "", 0, err
	}
	if stamp.BackendID == LocalBackend {
		return l.putLocal(ctx, &contextReader{ctx: ctx, reader: input})
	}
	f, hash, size, err := l.stage(ctx, input)
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	backend, err := l.OpenBackend(ctx, stamp.BackendID)
	if err != nil {
		return "", 0, err
	}
	defer backend.Close()
	if err = backend.PutFile(ctx, f, hash, size); err != nil {
		return "", 0, err
	}
	return hash, size, nil
}
func (l Library) stage(ctx context.Context, input io.Reader) (*os.File, string, int64, error) {
	dir := filepath.Join(l.StateDir, "resource-staging")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, "", 0, err
	}
	f, err := os.CreateTemp(dir, "original-*")
	if err != nil {
		return nil, "", 0, err
	}
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, sum), &contextReader{ctx: ctx, reader: input})
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, "", 0, err
	}
	return f, hex.EncodeToString(sum.Sum(nil)), n, nil
}
func (l Library) Read(ref Ref) ([]byte, error) { return l.ReadContext(context.Background(), ref) }
func (l Library) ReadContext(ctx context.Context, ref Ref) ([]byte, error) {
	f, err := l.Open(ctx, ref)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: f}, ref.Bytes+1))
}
func (l Library) Open(ctx context.Context, ref Ref) (*Original, error) {
	stamp, err := l.Stamp(ctx)
	if err != nil {
		return nil, err
	}
	if stamp.BackendID == LocalBackend {
		f, e := l.openLocal(ctx, ref)
		if e != nil {
			return nil, e
		}
		return &Original{File: f}, nil
	}
	backend, err := l.OpenBackend(ctx, stamp.BackendID)
	if err != nil {
		return nil, err
	}
	defer backend.Close()
	r, size, _, err := backend.Open(ctx, ref.Hash)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if size != ref.Bytes || size < 0 {
		return nil, errors.New("resource size does not match its reference")
	}
	f, hash, n, err := l.stage(ctx, io.LimitReader(r, size+1))
	if err != nil {
		return nil, err
	}
	if hash != ref.Hash || n != size {
		f.Close()
		os.Remove(f.Name())
		return nil, errors.New("resource content does not match its hash")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	return &Original{File: f, temporary: f.Name()}, nil
}
func (l Library) Stat(ctx context.Context, stamp Stamp, ref Ref) error {
	backend, err := l.OpenBackend(ctx, stamp.BackendID)
	if err != nil {
		return err
	}
	defer backend.Close()
	if stamp.BackendID == LocalBackend {
		path, err := l.Path(ref.Hash)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != ref.Bytes {
			return errors.New("resource file is unavailable")
		}
		return nil
	}
	b := backend.(*objectBackend)
	node, err := b.backend.Lookup(ctx, 1, ref.Hash)
	if err != nil {
		return err
	}
	if node.Size != ref.Bytes {
		return errors.New("resource size mismatch")
	}
	return nil
}
func (l Library) Files(ctx context.Context, visit func(File) error) error {
	stamp, err := l.Stamp(ctx)
	if err != nil {
		return err
	}
	files, err := l.BackendFiles(ctx, stamp.BackendID, true)
	if err != nil {
		return err
	}
	for _, f := range files {
		if err = visit(f); err != nil {
			return err
		}
	}
	return nil
}
func (l Library) BackendFiles(ctx context.Context, id string, includePending bool) ([]File, error) {
	backend, err := l.OpenBackend(ctx, id)
	if err != nil {
		return nil, err
	}
	defer backend.Close()
	return backend.Files(ctx, includePending)
}
func (l Library) RemoveFile(ctx context.Context, f File) error {
	backend, err := l.OpenBackend(ctx, f.BackendID)
	if err != nil {
		return err
	}
	defer backend.Close()
	return backend.Remove(ctx, f)
}

// CopyOriginal never takes the publication lock. Its durable migration task
// protects source/target deletion; immutable objects make unlocked reads safe.
func (l Library) CopyOriginal(ctx context.Context, source, target, hash string, size int64) (string, error) {
	src, err := l.OpenBackend(ctx, source)
	if err != nil {
		return "", err
	}
	defer src.Close()
	dst, err := l.OpenBackend(ctx, target)
	if err != nil {
		return "", err
	}
	defer dst.Close()
	r, n, _, err := src.Open(ctx, hash)
	if err != nil {
		return "", err
	}
	if n != size {
		r.Close()
		return "", errors.New("migration source size changed")
	}
	f, actual, n, err := l.stage(ctx, io.LimitReader(r, size+1))
	closeErr := r.Close()
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if closeErr != nil {
		return "", closeErr
	}
	if actual != hash || n != size {
		return "", errors.New("migration source integrity check failed")
	}
	if err = dst.PutFile(ctx, f, hash, size); err != nil {
		return "", err
	}
	check, n, locator, err := dst.Open(ctx, hash)
	if err != nil {
		return "", err
	}
	err = verifyReader(ctx, check, hash, size)
	closeErr = check.Close()
	if n != size {
		return "", errors.New("migration target size mismatch")
	}
	return locator, errors.Join(err, closeErr)
}
