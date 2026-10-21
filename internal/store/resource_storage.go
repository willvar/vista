package store

import (
	"context"
	"errors"
	"io/fs"
	"time"

	"vista/internal/resources"
)

type ResourceStorage struct {
	TotalBytes      int64  `json:"totalBytes"`
	TotalCount      int    `json:"totalCount"`
	ReferencedBytes int64  `json:"referencedBytes"`
	ReferencedCount int    `json:"referencedCount"`
	CleanableBytes  int64  `json:"cleanableBytes"`
	CleanableCount  int    `json:"cleanableCount"`
	ScannedAt       string `json:"scannedAt"`
}

// Session ownership is the persistent retention root, including attachments in
// compacted history, withdrawn inputs, and children of an active Session.
const resourceReferenced = `EXISTS(SELECT 1 FROM session_resources WHERE hash=?) OR EXISTS(SELECT 1 FROM message_resources WHERE hash=?) OR EXISTS(SELECT 1 FROM queued_input_resources WHERE hash=?)`

func (s *Store) ResourceStorage(ctx context.Context) (ResourceStorage, error) {
	unlock, err := s.library.Lock(ctx)
	if err != nil {
		return ResourceStorage{}, err
	}
	defer unlock()
	return s.resourceStorageLocked(ctx)
}

func (s *Store) resourceStorageLocked(ctx context.Context) (ResourceStorage, error) {
	var result ResourceStorage
	rows, err := s.db.QueryContext(ctx, `SELECT hash FROM session_resources UNION SELECT hash FROM message_resources UNION SELECT hash FROM queued_input_resources`)
	if err != nil {
		return result, err
	}
	used := map[string]bool{}
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			rows.Close()
			return result, err
		}
		used[hash] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	err = s.library.Files(ctx, func(file resources.File) error {
		result.TotalCount++
		result.TotalBytes += file.Bytes
		if used[file.Hash] {
			result.ReferencedCount++
			result.ReferencedBytes += file.Bytes
		} else {
			result.CleanableCount++
			result.CleanableBytes += file.Bytes
		}
		return nil
	})
	result.ScannedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return result, err
}

// CleanResources runs only on explicit user request. Publishing a file and
// committing its ownership use the same cross-process lock as this final check
// and unlink, so a concurrent upload cannot acquire a dangling reference.
func (s *Store) CleanResources(ctx context.Context, progress func(int, int64)) (int, int64, error) {
	var files []resources.File
	if err := s.library.Files(ctx, func(file resources.File) error { files = append(files, file); return nil }); err != nil {
		return 0, 0, err
	}
	count := 0
	var freed int64
	for _, file := range files {
		unlock, err := s.library.Lock(ctx)
		if err != nil {
			return count, freed, err
		}
		var used bool
		if file.Hash != "" {
			err = s.db.QueryRowContext(ctx, "SELECT "+resourceReferenced, file.Hash, file.Hash, file.Hash).Scan(&used)
		}
		if err == nil && !used {
			err = file.Remove()
			if err == nil {
				count++
				freed += file.Bytes
			} else if errors.Is(err, fs.ErrNotExist) {
				err = nil
			}
			if err == nil && file.Hash != "" {
				_, err = s.db.ExecContext(ctx, "DELETE FROM resources WHERE hash=?", file.Hash)
			}
		}
		unlock()
		if err != nil {
			return count, freed, err
		}
		if progress != nil {
			progress(count, freed)
		}
	}
	return count, freed, nil
}
