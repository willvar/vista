package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
)

type PatchInput struct {
	Directory      string            `json:"directory"`
	Patch          string            `json:"patch"`
	ExpectedSHA256 map[string]string `json:"expectedSha256"`
}

type PatchFile struct {
	Path    string   `json:"path"`
	OldPath string   `json:"oldPath,omitempty"`
	SHA256  string   `json:"sha256,omitempty"`
	Deleted bool     `json:"deleted,omitempty"`
	Diff    FileDiff `json:"diff"`
}

type PatchResult struct {
	Files     []PatchFile `json:"files"`
	Applied   []string    `json:"applied"`
	Unapplied []string    `json:"unapplied,omitempty"`
}

type patchOperation struct {
	file                      *gitdiff.File
	old, next                 string
	oldExpected, nextExpected string
	mode                      os.FileMode
	staged                    string
	result                    PatchFile
}

// Traditional diff -u has no prefix metadata. Normalize the conventional
// matching a/path and b/path pair before go-gitdiff chooses its target name.
// Git headers retain their own explicit prefix/rename information.
var unifiedHunkHeader = regexp.MustCompile(`^@@ -[0-9]+(?:,([0-9]+))? \+[0-9]+(?:,([0-9]+))? @@`)

func parseUnifiedPatch(patch string) ([]*gitdiff.File, error) {
	lines := strings.SplitAfter(patch, "\n")
	gitHeader, oldLeft, newLeft := false, 0, 0
	name := func(line string) string {
		value, _, _ := strings.Cut(strings.TrimSuffix(line[4:], "\n"), "\t")
		if strings.HasPrefix(value, `"`) {
			if decoded, err := strconv.Unquote(value); err == nil {
				return decoded
			}
		}
		return value
	}
	for i, line := range lines {
		if oldLeft > 0 || newLeft > 0 {
			switch {
			case strings.HasPrefix(line, " "):
				oldLeft--
				newLeft--
			case strings.HasPrefix(line, "-"):
				oldLeft--
			case strings.HasPrefix(line, "+"):
				newLeft--
			}
			continue
		}
		if hunk := unifiedHunkHeader.FindStringSubmatch(line); hunk != nil {
			oldLeft, newLeft = 1, 1
			if hunk[1] != "" {
				oldLeft, _ = strconv.Atoi(hunk[1])
			}
			if hunk[2] != "" {
				newLeft, _ = strconv.Atoi(hunk[2])
			}
			continue
		}
		if strings.HasPrefix(line, "diff --git ") {
			gitHeader = true
			continue
		}
		if i+1 >= len(lines) || !strings.HasPrefix(line, "--- ") || !strings.HasPrefix(lines[i+1], "+++ ") {
			continue
		}
		if gitHeader {
			gitHeader = false
			continue
		}
		old, next := name(line), name(lines[i+1])
		if strings.HasPrefix(old, "a/") && strings.HasPrefix(next, "b/") && old[2:] == next[2:] {
			old, next = old[2:], next[2:]
		} else if old == "/dev/null" {
			next = strings.TrimPrefix(next, "b/")
		} else if next == "/dev/null" {
			old = strings.TrimPrefix(old, "a/")
		}
		lines[i] = "--- " + strconv.Quote(old) + "\n"
		lines[i+1] = "+++ " + strconv.Quote(next) + "\n"
	}
	files, _, err := gitdiff.Parse(strings.NewReader(strings.Join(lines, "")))
	return files, err
}

func patchNames(file *gitdiff.File) (string, string) {
	old, next := file.OldName, file.NewName
	if file.IsNew || old == "/dev/null" {
		old = ""
	}
	if file.IsDelete || next == "/dev/null" {
		next = ""
	}
	return old, next
}

func patchPath(directory, name string) (string, error) {
	if name == "" {
		return "", nil
	}
	if strings.Contains(name, "\\") || !filepath.IsLocal(filepath.FromSlash(name)) {
		return "", errors.New("patch paths must be relative to directory")
	}
	path, err := canonicalWritePath(filepath.Join(directory, filepath.FromSlash(name)))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(directory, path)
	if err != nil || !filepath.IsLocal(relative) {
		return "", errors.New("patch path escapes directory through a symlink")
	}
	return path, nil
}

// PatchPaths performs the same parsing/path validation used by application.
// The agent checks all affected rule scopes before executing any operation.
func PatchPaths(input PatchInput) ([]string, error) {
	directory, err := canonicalWritePath(input.Directory)
	if err != nil {
		return nil, err
	}
	files, err := parseUnifiedPatch(input.Patch)
	if err != nil {
		return nil, fmt.Errorf("parse unified diff: %w", err)
	}
	if len(files) == 0 {
		return nil, errors.New("patch contains no file changes")
	}
	var paths []string
	seen := make(map[string]bool)
	for _, file := range files {
		old, next := patchNames(file)
		for _, name := range []string{old, next} {
			path, err := patchPath(directory, name)
			if err != nil {
				return nil, err
			}
			if path != "" && !seen[path] {
				seen[path] = true
				paths = append(paths, path)
			}
		}
	}
	return paths, nil
}

func checkExpected(ctx context.Context, path, expected string, capture io.Writer) error {
	if expected != "missing" {
		if len(expected) != 64 {
			return errors.New("expectedSha256 must contain the hash from read_file, or missing for a new path")
		}
		if _, err := hex.DecodeString(expected); err != nil {
			return err
		}
	}
	current, err := fileDigestTo(ctx, path, capture)
	if expected == "missing" {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err == nil {
			return ErrConflict
		}
		return err
	}
	if errors.Is(err, os.ErrNotExist) || err == nil && current != expected {
		return ErrConflict
	}
	return err
}

// Every patch is staged before the first project write, then every source is
// rechecked under the shared cross-Worker locks. External processes can bypass
// those locks, just as for write_file. Atomicity is per destination file.
func ApplyPatch(ctx context.Context, stateDir string, input PatchInput) (PatchResult, error) {
	result := PatchResult{Files: []PatchFile{}, Applied: []string{}}
	directory, err := canonicalWritePath(input.Directory)
	if err != nil {
		return result, err
	}
	files, err := parseUnifiedPatch(input.Patch)
	if err != nil {
		return result, fmt.Errorf("parse unified diff: %w", err)
	}
	if len(files) == 0 {
		return result, errors.New("patch contains no file changes")
	}
	staging := filepath.Join(stateDir, "outputs")
	if err := os.MkdirAll(staging, 0700); err != nil {
		return result, err
	}
	var operations []*patchOperation
	defer func() {
		for _, op := range operations {
			if op.staged != "" {
				os.Remove(op.staged)
			}
		}
	}()
	var paths []string
	seen := map[string]bool{}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if file.IsBinary || file.OldMode.Type() != 0 || file.NewMode.Type() != 0 {
			return result, errors.New("only regular UTF-8 text patches are supported")
		}
		oldName, nextName := patchNames(file)
		old, err := patchPath(directory, oldName)
		if err != nil {
			return result, err
		}
		next, err := patchPath(directory, nextName)
		if err != nil {
			return result, err
		}
		if old == "" && next == "" {
			return result, errors.New("patch has no file path")
		}
		op := &patchOperation{file: file, old: old, next: next, mode: 0600}
		operations = append(operations, op)
		opPaths := []string{old}
		if next != old {
			opPaths = append(opPaths, next)
		}
		for _, path := range opPaths {
			if path == "" {
				continue
			}
			if seen[path] {
				return result, errors.New("each file may occur only once per patch; combine its hunks")
			}
			seen[path] = true
			paths = append(paths, path)
		}
		if err := stagePatchOperation(ctx, staging, op, input.ExpectedSHA256); err != nil {
			return result, err
		}
	}
	release, err := lockFilePaths(ctx, stateDir, paths)
	if err != nil {
		return result, err
	}
	defer release()
	for _, op := range operations {
		if op.old != "" {
			if err := checkExpected(ctx, op.old, op.oldExpected, io.Discard); err != nil {
				return result, fmt.Errorf("%s: %w", op.old, err)
			}
		}
		if op.next != "" && op.next != op.old {
			if err := checkExpected(ctx, op.next, op.nextExpected, io.Discard); err != nil {
				return result, fmt.Errorf("%s: %w", op.next, err)
			}
		}
	}
	return commitPatchOperations(ctx, operations)
}

// Stage one file at a time. Only paths survive preflight, so source and
// temporary descriptors do not accumulate alongside the cross-Worker locks.
func stagePatchOperation(ctx context.Context, staging string, op *patchOperation, hashes map[string]string) error {
	file, old, next := op.file, op.old, op.next
	oldName, nextName := patchNames(file)
	before := &diffCapture{}
	var source io.ReaderAt = strings.NewReader("")
	if old != "" {
		op.oldExpected = hashes[oldName]
		if err := checkExpected(ctx, old, op.oldExpected, before); err != nil {
			return fmt.Errorf("%s: %w", oldName, err)
		}
		file, err := os.Open(old)
		if err != nil {
			return err
		}
		defer file.Close()
		source = file
		info, err := file.Stat()
		if err != nil {
			return err
		}
		op.mode = info.Mode().Perm()
	}
	if next != "" && next != old {
		op.nextExpected = hashes[nextName]
		if op.nextExpected != "missing" {
			return fmt.Errorf("%s: new or moved destination must use expectedSha256 missing", nextName)
		}
		if err := checkExpected(ctx, next, op.nextExpected, io.Discard); err != nil {
			return fmt.Errorf("%s: %w", nextName, err)
		}
	}
	if file.NewMode != 0 {
		op.mode = file.NewMode.Perm()
	}
	staged, err := os.CreateTemp(staging, "patch-*")
	if err != nil {
		return err
	}
	op.staged = staged.Name()
	defer staged.Close()
	hash := sha256.New()
	if err := gitdiff.Apply(io.MultiWriter(staged, hash), patchContextReaderAt{ctx, source}, file); err != nil {
		return fmt.Errorf("%s: %w; reread the file and supply correct hunk line numbers and context", oldName, err)
	}
	if _, err := staged.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := scanText(ctx, staged, func([]byte, bool) error { return nil }); err != nil {
		return err
	}
	op.result = PatchFile{Path: next, SHA256: hex.EncodeToString(hash.Sum(nil)), Deleted: next == ""}
	if next == "" {
		op.result.Path, op.result.SHA256 = old, ""
	}
	if old != "" && next != "" && old != next {
		op.result.OldPath = old
	}
	info, err := staged.Stat()
	if err != nil {
		return err
	}
	if info.Size() <= maxFileBytes {
		if _, err := staged.Seek(0, io.SeekStart); err != nil {
			return err
		}
		text, err := io.ReadAll(staged)
		if err != nil {
			return err
		}
		op.result.Diff = makeFileChangeDiff(op.result.Path, old == "" || file.IsCopy, next == "", before, string(text))
	} else {
		op.result.Diff = FileDiff{Path: op.result.Path, Omitted: "too_large"}
	}
	op.result.Diff.Deleted = next == ""
	return nil
}

func commitPatchOperations(ctx context.Context, operations []*patchOperation) (PatchResult, error) {
	result := PatchResult{Files: []PatchFile{}, Applied: []string{}}
	for i, op := range operations {
		wrote := false
		err := ctx.Err()
		if err == nil && op.next != "" {
			err = commitStagedFile(ctx, op.next, op.mode, op.staged)
			wrote = err == nil
		}
		if err == nil && op.old != "" && op.old != op.next && !op.file.IsCopy {
			err = os.Remove(op.old)
		}
		if err != nil {
			if wrote {
				actual := op.result
				actual.OldPath = ""
				// Destination exists, source removal failed: the committed part is a
				// creation, not a completed rename. Only the source remains unapplied.
				actual.Diff.Created = true
				if staged, openErr := os.Open(op.staged); openErr == nil {
					text, readErr := io.ReadAll(io.LimitReader(staged, maxFileBytes+1))
					staged.Close()
					if readErr == nil && len(text) <= maxFileBytes {
						actual.Diff = makeFileChangeDiff(op.next, true, false, &diffCapture{}, string(text))
					}
				}
				result.Files = append(result.Files, actual)
				result.Applied = append(result.Applied, op.next)
				result.Unapplied = append(result.Unapplied, op.old)
			} else {
				result.Unapplied = append(result.Unapplied, op.result.Path)
			}
			for _, pending := range operations[i+1:] {
				result.Unapplied = append(result.Unapplied, pending.result.Path)
			}
			return result, err
		}
		result.Files = append(result.Files, op.result)
		result.Applied = append(result.Applied, op.result.Path)
	}
	return result, nil
}

func commitStagedFile(ctx context.Context, path string, mode os.FileMode, sourcePath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".vista-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if err := temp.Chmod(mode); err != nil {
		return err
	}
	if _, err := io.Copy(temp, webContextReader{ctx, source}); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

type patchContextReaderAt struct {
	context.Context
	io.ReaderAt
}

func (r patchContextReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	if err := r.Err(); err != nil {
		return 0, err
	}
	return r.ReaderAt.ReadAt(p, offset)
}
