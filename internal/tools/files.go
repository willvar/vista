package tools

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"vista/internal/platform"
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
	if err := ctx.Err(); err != nil {
		return "", err
	}
	path, err := absolute(path)
	if err != nil {
		return "", err
	}
	if len(content) > maxFileBytes || !utf8.ValidString(content) {
		return "", errors.New("new file exceeds 1 MiB or is not UTF-8 text")
	}
	if expectedSHA256 != "missing" {
		if len(expectedSHA256) != 64 {
			return "", errors.New("expected SHA-256 must be 64 hexadecimal characters or 'missing'")
		}
		if _, err := hex.DecodeString(expectedSHA256); err != nil {
			return "", errors.New("invalid expected SHA-256")
		}
	}
	if stateDir == "" {
		return "", errors.New("state directory is required")
	}
	realDir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	path = filepath.Join(realDir, filepath.Base(path))
	if actual, err := filepath.EvalSymlinks(path); err == nil {
		path = actual
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	lockDir := filepath.Join(stateDir, "file-locks")
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return "", err
	}
	lockHash := sha256.Sum256([]byte(path))
	lock, err := os.OpenFile(filepath.Join(lockDir, hex.EncodeToString(lockHash[:])+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	for {
		if err := platform.TryLock(lock); err == nil {
			break
		} else if !platform.IsLockBusy(err) {
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	current, err := fileDigest(ctx, path)
	if expectedSHA256 == "missing" {
		if err == nil {
			return "", ErrConflict
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	} else {
		if errors.Is(err, os.ErrNotExist) || (err == nil && current != expectedSHA256) {
			return "", ErrConflict
		}
		if err != nil {
			return "", err
		}
	}
	mode := os.FileMode(0600)
	if expectedSHA256 != "missing" {
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(realDir, ".vista-write-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(mode); err != nil {
		return "", err
	}
	if _, err := io.WriteString(tmp, content); err != nil {
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	newHash := sha256.Sum256([]byte(content))
	return hex.EncodeToString(newHash[:]), nil
}

// Hash existing text without reintroducing a whole-file read limit when a
// numbered page from a large file is used for an optimistic replacement.
func fileDigest(ctx context.Context, path string) (string, error) {
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
	if err := scanText(ctx, io.TeeReader(file, hash), func([]byte, bool) error { return nil }); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type Match struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}
type SearchResult struct {
	Matches      []Match `json:"matches"`
	Truncated    bool    `json:"truncated"`
	FilesScanned int     `json:"filesScanned"`
}

func Search(ctx context.Context, root, pattern string) (SearchResult, error) {
	root, err := absolute(root)
	if err != nil {
		return SearchResult{}, err
	}
	if len(pattern) == 0 || len(pattern) > 512 {
		return SearchResult{}, errors.New("search pattern must be 1–512 bytes")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return SearchResult{}, err
	}
	ignores, err := newSearchIgnore(root)
	if err != nil {
		return SearchResult{}, fmt.Errorf("search %s: %w", root, err)
	}
	result := SearchResult{Matches: []Match{}}
	var bytesRead int64
	errLimit := errors.New("search limit reached")
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			return walkErr
		}
		// WalkDir is depth-first; discard scopes from the preceding branch.
		for ignores.directory != path && ignores.directory != filepath.Dir(path) {
			ignores = ignores.parent
		}
		if ignores.ignored(path, entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if path != root && (entry.Name() == ".git" || entry.Name() == "node_modules") {
				return filepath.SkipDir
			}
			if ignores.directory != path {
				ignores, err = ignores.descend(path)
				return err
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			result.Truncated = true
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if result.FilesScanned >= maxSearchFiles || bytesRead >= maxSearchBytes {
			result.Truncated = true
			return errLimit
		}
		f, err := os.Open(path)
		if err != nil {
			result.Truncated = true
			return nil
		}
		defer f.Close()
		result.FilesScanned++
		remaining := int64(maxSearchBytes) - bytesRead
		fileMatches := []Match{}
		offset, lineStart, line := int64(0), int64(0), 1
		var prefix strings.Builder
		binary := false
		scanErr := scanText(ctx, io.LimitReader(f, remaining), func(fragment []byte, end bool) error {
			if strings.IndexByte(string(fragment), 0) >= 0 {
				binary = true
			}
			if prefix.Len() < 512 {
				prefix.Write(fragment[:min(len(fragment), 512-prefix.Len())])
			}
			offset += int64(len(fragment))
			if end {
				if info.Size() > remaining && offset >= remaining {
					// A budget boundary is not the end of a source line.
					return errLimit
				}
				if !binary && re.MatchReader(bufio.NewReader(webContextReader{ctx, io.NewSectionReader(f, lineStart, offset-lineStart)})) {
					text := prefix.String()
					for !utf8.ValidString(text) {
						text = text[:len(text)-1]
					}
					fileMatches = append(fileMatches, Match{Path: path, Line: line, Text: text})
					if len(result.Matches)+len(fileMatches) > maxSearchResults {
						return errLimit
					}
				}
				line++
				offset++ // delimiter; final synthetic newline is corrected below
				lineStart = offset
				prefix.Reset()
			}
			return nil
		})
		position, _ := f.Seek(0, io.SeekCurrent)
		bytesRead += position
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if scanErr != nil && info.Size() > remaining && position >= remaining {
			// Cutting inside a UTF-8 rune must still expose completed matches
			// and report a partial scan, rather than silently dropping the file.
			scanErr = errLimit
		}
		if scanErr != nil && !errors.Is(scanErr, errLimit) {
			// Invalid UTF-8/binary files are not text-search candidates.
			if !strings.Contains(scanErr.Error(), "not UTF-8") {
				result.Truncated = true
			}
			return nil
		}
		if !binary {
			result.Matches = append(result.Matches, fileMatches[:min(len(fileMatches), maxSearchResults-len(result.Matches))]...)
		}
		if errors.Is(scanErr, errLimit) || info.Size() > remaining {
			result.Truncated = true
			return errLimit
		}
		return nil
	})
	if errors.Is(err, errLimit) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("search %s: %w", root, err)
	}
	return result, nil
}
