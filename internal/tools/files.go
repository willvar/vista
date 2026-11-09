package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

const maxFileBytes = 1 << 20
const maxSearchFiles = 10000
const maxSearchBytes = 32 << 20
const maxSearchResults = 100

var ErrConflict = errors.New("file changed since it was read")

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	SHA256  string `json:"sha256"`
}

func absolute(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("an absolute path is required")
	}
	return filepath.Clean(path), nil
}

func ReadFile(path string) (File, error) {
	path, err := absolute(path)
	if err != nil {
		return File{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return File{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return File{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileBytes {
		return File{}, errors.New("file must be regular and at most 1 MiB")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return File{}, err
	}
	if len(b) > maxFileBytes || !utf8.Valid(b) {
		return File{}, errors.New("file exceeds 1 MiB or is not UTF-8 text")
	}
	hash := sha256.Sum256(b)
	return File{Path: path, Content: string(b), SHA256: hex.EncodeToString(hash[:])}, nil
}

// WriteFile serializes Vista writes across Workers through a lock in stateDir.
// Shell commands and external processes can bypass the lock, so the digest is
// an optimistic check rather than a guarantee against every external race.
func WriteFile(ctx context.Context, stateDir, path, expectedSHA256, content string) (string, error) {
	result, err := WriteFileWithDiff(ctx, stateDir, path, expectedSHA256, content)
	return result.SHA256, err
}

func WriteFileWithDiff(ctx context.Context, stateDir, path, expectedSHA256, content string) (FileWrite, error) {
	if err := ctx.Err(); err != nil {
		return FileWrite{}, err
	}
	path, err := absolute(path)
	if err != nil {
		return FileWrite{}, err
	}
	if len(content) > maxFileBytes || !utf8.ValidString(content) {
		return FileWrite{}, errors.New("new file exceeds 1 MiB or is not UTF-8 text")
	}
	if expectedSHA256 != "missing" {
		if len(expectedSHA256) != 64 {
			return FileWrite{}, errors.New("expected SHA-256 must be 64 hexadecimal characters or 'missing'")
		}
		if _, err := hex.DecodeString(expectedSHA256); err != nil {
			return FileWrite{}, errors.New("invalid expected SHA-256")
		}
	}
	if stateDir == "" {
		return FileWrite{}, errors.New("state directory is required")
	}
	realDir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return FileWrite{}, err
	}
	path = filepath.Join(realDir, filepath.Base(path))
	if actual, err := filepath.EvalSymlinks(path); err == nil {
		path = actual
	} else if !errors.Is(err, os.ErrNotExist) {
		return FileWrite{}, err
	}
	release, err := lockFilePaths(ctx, stateDir, []string{path})
	if err != nil {
		return FileWrite{}, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return FileWrite{}, err
	}
	before := &diffCapture{}
	current, err := fileDigestTo(ctx, path, before)
	if expectedSHA256 == "missing" {
		if err == nil {
			return FileWrite{}, ErrConflict
		}
		if !errors.Is(err, os.ErrNotExist) {
			return FileWrite{}, err
		}
	} else {
		if errors.Is(err, os.ErrNotExist) || (err == nil && current != expectedSHA256) {
			return FileWrite{}, ErrConflict
		}
		if err != nil {
			return FileWrite{}, err
		}
	}
	mode := os.FileMode(0600)
	if expectedSHA256 != "missing" {
		info, err := os.Stat(path)
		if err != nil {
			return FileWrite{}, err
		}
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(realDir, ".vista-write-*")
	if err != nil {
		return FileWrite{}, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(mode); err != nil {
		return FileWrite{}, err
	}
	if _, err := io.WriteString(tmp, content); err != nil {
		return FileWrite{}, err
	}
	if err := tmp.Sync(); err != nil {
		return FileWrite{}, err
	}
	if err := tmp.Close(); err != nil {
		return FileWrite{}, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return FileWrite{}, err
	}
	newHash := sha256.Sum256([]byte(content))
	return FileWrite{Path: path, SHA256: hex.EncodeToString(newHash[:]), Diff: makeFileDiff(path, expectedSHA256 == "missing", before, content)}, nil
}

// Hash existing text without reintroducing a whole-file read limit when a
// numbered page from a large file is used for an optimistic replacement.
func fileDigest(ctx context.Context, path string) (string, error) {
	return fileDigestTo(ctx, path, io.Discard)
}

func fileDigestTo(ctx context.Context, path string, capture io.Writer) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("file must be regular")
	}
	hash := sha256.New()
	if err := scanText(ctx, io.TeeReader(file, io.MultiWriter(hash, capture)), func([]byte, bool) error { return nil }); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Search retains the original content-search entry point for internal callers.
func Search(ctx context.Context, root, pattern string) (SearchResult, error) {
	return SearchFiles(ctx, SearchOptions{Root: root, Pattern: pattern})
}
