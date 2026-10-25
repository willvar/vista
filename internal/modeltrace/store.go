package modeltrace

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const directory = "model-diagnostics"
const RetentionDays = 7
const maxStorageBytes = 128 << 20
const maxRecords = 256
const maxRecordFileBytes = 24 << 20 // Includes JSON escaping of captured bytes.

type Summary struct {
	Scope
	ID         string    `json:"id"`
	Provider   string    `json:"provider"`
	Protocol   string    `json:"protocol"`
	Model      string    `json:"model"`
	StartedAt  time.Time `json:"startedAt"`
	DurationMS float64   `json:"durationMs"`
	Error      string    `json:"error,omitempty"`
	Truncated  bool      `json:"truncated"`
}

func (r Record) Summary() Summary {
	return Summary{Scope: r.Scope, ID: r.ID, Provider: r.Provider, Protocol: r.Protocol, Model: r.Model, StartedAt: r.StartedAt, DurationMS: r.DurationMS, Error: r.Error, Truncated: r.Truncated}
}

func validID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value
}

func recordPath(sessionID, runID, id string) (string, error) {
	if !validID(sessionID) || !validID(runID) || !validID(id) {
		return "", fs.ErrNotExist
	}
	return filepath.Join(sessionID, runID, id+".jsonl"), nil
}

// Each file starts with a small summary line, followed by the bounded record.
// Listing requests never loads their potentially large prompts and responses.
func Save(stateDir string, record Record) error {
	path, err := recordPath(record.SessionID, record.RunID, record.ID)
	if err != nil {
		return err
	}
	dir := filepath.Join(stateDir, directory)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	// Random, exclusive temporary paths support concurrent resident Workers.
	tmp := path + "." + uuid.NewString() + ".tmp"
	file, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	encoder := json.NewEncoder(file)
	summary := record.Summary()
	if err = encoder.Encode(summary); err == nil {
		err = encoder.Encode(record)
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := root.Rename(tmp, path); err != nil {
		return err
	}
	prune(root, time.Now())
	return nil
}

// ReadSummary confirms a saved record without loading its captured HTTP bodies.
func ReadSummary(stateDir, sessionID, runID, id string) (Summary, error) {
	path, err := recordPath(sessionID, runID, id)
	if err != nil {
		return Summary{}, err
	}
	root, err := os.OpenRoot(filepath.Join(stateDir, directory))
	if err != nil {
		return Summary{}, err
	}
	defer root.Close()
	file, err := root.Open(path)
	if err != nil {
		return Summary{}, err
	}
	defer file.Close()
	line, err := bufio.NewReader(io.LimitReader(file, 128<<10)).ReadBytes('\n')
	var summary Summary
	if err != nil {
		return summary, err
	}
	if err := json.Unmarshal(line, &summary); err != nil {
		return summary, err
	}
	if summary.ID != id || summary.SessionID != sessionID || summary.RunID != runID {
		return Summary{}, fs.ErrNotExist
	}
	return summary, nil
}

func List(stateDir, sessionID, runID string) ([]Summary, error) {
	if !validID(sessionID) || !validID(runID) {
		return nil, fs.ErrNotExist
	}
	result := []Summary{}
	root, err := os.OpenRoot(filepath.Join(stateDir, directory))
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir := filepath.Join(sessionID, runID)
	folder, err := root.Open(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	defer folder.Close()
	entries, err := folder.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		file, err := root.Open(filepath.Join(dir, entry.Name()))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		} // Concurrent retention cleanup.
		if err != nil {
			return nil, err
		}
		line, readErr := bufio.NewReader(io.LimitReader(file, 128<<10)).ReadBytes('\n')
		file.Close()
		var summary Summary
		if readErr != nil || json.Unmarshal(line, &summary) != nil || summary.SessionID != sessionID || summary.RunID != runID || !validID(summary.ID) {
			continue
		}
		if time.Since(summary.StartedAt) > RetentionDays*24*time.Hour {
			continue
		}
		result = append(result, summary)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].StartedAt.Before(result[j].StartedAt) })
	return result, nil
}

func Read(stateDir, sessionID, runID, id string) (Record, error) {
	path, err := recordPath(sessionID, runID, id)
	if err != nil {
		return Record{}, err
	}
	root, err := os.OpenRoot(filepath.Join(stateDir, directory))
	if err != nil {
		return Record{}, err
	}
	defer root.Close()
	file, err := root.Open(path)
	if err != nil {
		return Record{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Record{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxRecordFileBytes {
		return Record{}, errors.New("invalid diagnostic record size")
	}
	reader := bufio.NewReader(io.LimitReader(file, maxRecordFileBytes+1))
	if _, err := reader.ReadBytes('\n'); err != nil {
		return Record{}, err
	}
	var record Record
	if err := json.NewDecoder(reader).Decode(&record); err != nil {
		return Record{}, err
	}
	if record.ID != id || record.SessionID != sessionID || record.RunID != runID || time.Since(record.StartedAt) > RetentionDays*24*time.Hour {
		return Record{}, fs.ErrNotExist
	}
	return record, nil
}

func DeleteSession(stateDir, sessionID string) error {
	if !validID(sessionID) {
		return fs.ErrNotExist
	}
	root, err := os.OpenRoot(filepath.Join(stateDir, directory))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	return root.RemoveAll(sessionID)
}

func Prune(stateDir string) {
	root, err := os.OpenRoot(filepath.Join(stateDir, directory))
	if err != nil {
		return
	}
	defer root.Close()
	prune(root, time.Now())
}

func prune(root *os.Root, now time.Time) {
	type stored struct {
		path     string
		size     int64
		modified time.Time
	}
	var files []stored
	var dirs []string
	_ = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() && path != "." {
			dirs = append(dirs, path)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if strings.HasSuffix(path, ".tmp") {
			if now.Sub(info.ModTime()) > 24*time.Hour {
				_ = root.Remove(path)
			}
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if now.Sub(info.ModTime()) > RetentionDays*24*time.Hour {
			_ = root.Remove(path)
			return nil
		}
		files = append(files, stored{path, info.Size(), info.ModTime()})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].modified.After(files[j].modified) })
	var total int64
	for i, file := range files {
		total += file.size
		if i >= maxRecords || total > maxStorageBytes {
			_ = root.Remove(file.path)
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = root.Remove(dirs[i])
	} // Remove only empty directories.
}
