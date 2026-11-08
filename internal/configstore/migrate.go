package configstore

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

type legacyFile struct {
	binding binding
	data    []byte
	present bool
}

func collectLegacy(root *os.Root) (*document, []legacyFile, error) {
	doc := &document{Version: LegacyVersion}
	var files []legacyFile
	for _, b := range bindings {
		data, _, err := readFile(root, b.legacy, b.limit, b.section == Models || b.section == MCP)
		if errors.Is(err, os.ErrNotExist) {
			files = append(files, legacyFile{binding: b})
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read legacy %s configuration: %w", b.section, err)
		}
		value, err := compact(data)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid legacy %s configuration", b.section)
		}
		*doc.field(b.section) = value
		files = append(files, legacyFile{binding: b, data: data, present: true})
	}
	return doc, files, nil
}

type backupFile struct {
	name    string
	data    []byte
	present bool
}

// Exact source bytes stay under the private root. Unified-v1 and key-file
// snapshots are included when those are migration inputs; authorizations and
// AGENTS.md are never copied into this settings backup.
func prepareBackup(root *os.Root, files []legacyFile, extra ...backupFile) error {
	hasFiles := false
	for _, file := range files {
		hasFiles = hasFiles || file.present
	}
	for _, file := range extra {
		hasFiles = hasFiles || file.present
	}
	verify := func() error {
		if err := verifyLegacy(root, files); err != nil {
			return err
		}
		for _, file := range extra {
			if err := verifySnapshot(root, file.name, file.data, file.present); err != nil {
				return err
			}
		}
		return nil
	}
	if !hasFiles {
		return verify()
	}
	name := ".config-backup-" + time.Now().UTC().Format("20060102T150405Z") + "-" + uuid.NewString()
	if err := root.Mkdir(name, 0700); err != nil {
		return err
	}
	var copies []backupFile
	for _, file := range files {
		if file.present {
			copies = append(copies, backupFile{name: file.binding.legacy, data: file.data, present: true})
		}
	}
	copies = append(copies, extra...)
	var sums strings.Builder
	for _, file := range copies {
		if !file.present {
			continue
		}
		path := name + "/" + file.name
		if err := writePrivate(root, path, file.data); err != nil {
			return err
		}
		saved, _, err := readFile(root, path, len(file.data), true)
		if err != nil || !bytes.Equal(saved, file.data) {
			return errors.New("configuration backup verification failed")
		}
		fmt.Fprintf(&sums, "%s  %s\n", digest(file.data), file.name)
	}
	if err := writePrivate(root, name+"/SHA256SUMS", []byte(sums.String())); err != nil {
		return err
	}
	if err := syncDirectory(root, name); err != nil {
		return err
	}
	if err := syncDirectory(root, "."); err != nil {
		return err
	}
	return verify()
}

// Snapshot absence as well as bytes. A legacy file created during preparation
// must cause a retry, not become an unbacked-up, ignored setting after commit.
func verifyLegacy(root *os.Root, files []legacyFile) error {
	for _, file := range files {
		current, _, err := readFile(root, file.binding.legacy, file.binding.limit, file.binding.section == Models || file.binding.section == MCP)
		if errors.Is(err, os.ErrNotExist) && !file.present {
			continue
		}
		if err != nil || !file.present || !bytes.Equal(current, file.data) {
			return ErrConflict
		}
	}
	return nil
}
func retireLegacy(root *os.Root, files []legacyFile) error {
	if len(files) == 0 {
		return nil
	}
	if err := verifyLegacy(root, files); err != nil {
		return errors.New("legacy configuration changed after migration; config.json is authoritative")
	}
	removed := false
	for _, file := range files {
		if !file.present {
			continue
		}
		if err := root.Remove(file.binding.legacy); err != nil {
			return err
		}
		removed = true
	}
	if removed {
		return syncDirectory(root, ".")
	}
	return nil
}

// Migrate runs under the Server instance lock before loading settings. Once
// config.json exists it wins completely; old files are never merged back in.
func Migrate(dir string) error {
	key, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	root, err := openRoot(key, true)
	if err != nil {
		return err
	}
	defer root.Close()
	guard, err := lock(root)
	if err != nil {
		return err
	}
	defer guard.Close()
	state, err := loadStartupState(root, key)
	if err != nil {
		return err
	}
	if state.existed && state.public.Version == Version && state.keys.exists {
		return nil
	}
	if err := backupState(root, state); err != nil {
		return err
	}
	return completePairCommit(root, key, state)
}

// Reset is used only by the confirmed full-reset workflow. Clear and creation
// of the new empty baseline share the writer lock; clear must retain state/config.lock.
func Reset(dir string, clear func(*os.Root) error) error {
	key, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	root, err := openRoot(key, true)
	if err != nil {
		return err
	}
	defer root.Close()
	guard, err := lock(root)
	if err != nil {
		return err
	}
	defer guard.Close()
	if err := clear(root); err != nil {
		invalidate(key)
		return err
	}
	return completePairCommit(root, key, &storedState{raw: &document{Version: LegacyVersion}, keys: keySnapshot{doc: emptyKeys()}})
}

// PreviewServer validates startup inputs without changing files. The CLI uses
// it before stopping a running instance; only Server startup performs migration.
func PreviewServer(dir string) ([]byte, bool, error) {
	key, err := filepath.Abs(dir)
	if err != nil {
		return nil, false, err
	}
	root, err := openRoot(key, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer root.Close()
	state, err := loadStartupState(root, key)
	if err != nil {
		return nil, false, err
	}
	public, keys, _, err := separateDocument(state.raw, state.public, state.keys.doc)
	if err != nil {
		return nil, false, err
	}
	data, err := marshalConfiguration(public, true)
	if err != nil || len(data) > maxDocumentBytes {
		return nil, false, errors.New("invalid startup configuration")
	}
	if _, err := decode(data); err != nil {
		return nil, false, err
	}
	if _, err := encodeKeys(keys); err != nil {
		return nil, false, err
	}
	value := state.raw.Server
	return bytes.Clone(value), len(value) != 0, nil
}

// Missing-current-file diagnostics look only at entry existence, never at old
// values. They prevent accidental initialization over an unmigrated profile.
func unmigratedInputs(root *os.Root) (bool, error) {
	for _, b := range bindings {
		if _, err := root.Lstat(b.legacy); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	if _, err := root.Lstat(KeyFilename); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return false, nil
}
