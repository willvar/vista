package store

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

const resourceReferenced = `EXISTS(SELECT 1 FROM session_resources WHERE hash=?) OR EXISTS(SELECT 1 FROM message_resources WHERE hash=?) OR EXISTS(SELECT 1 FROM queued_input_resources WHERE hash=?) OR EXISTS(SELECT 1 FROM draft_resources WHERE hash=?)`

func (s *Store) ResourceStorage(ctx context.Context) (ResourceStorage, error) {
	release, err := s.library.Lock(ctx)
	if err != nil {
		return ResourceStorage{}, err
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	control := s.ResourceControl()
	stamp, err := control.Current(ctx)
	if err != nil {
		return ResourceStorage{}, err
	}
	ids, err := control.BackendIDs(ctx)
	if err != nil {
		return ResourceStorage{}, err
	}
	rows, err := s.orm.WithContext(ctx).Raw(`SELECT hash FROM session_resources UNION SELECT hash FROM message_resources UNION SELECT hash FROM queued_input_resources UNION SELECT hash FROM draft_resources`).Rows()
	if err != nil {
		return ResourceStorage{}, err
	}
	used := map[string]bool{}
	for rows.Next() {
		var hash string
		if err = rows.Scan(&hash); err != nil {
			rows.Close()
			return ResourceStorage{}, err
		}
		used[hash] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ResourceStorage{}, err
	}
	release()
	release = nil
	var result ResourceStorage
	// Statistics are a revision-recovered snapshot, not a cutover proof. Do not
	// hold the publication lock or an SQL cursor over remote inventory requests.
	for _, id := range ids {
		protected, err := control.Protected(ctx, id)
		if err != nil {
			return result, err
		}
		files, err := s.library.BackendFiles(ctx, id, true)
		if err != nil {
			return result, err
		}
		for _, file := range files {
			result.TotalCount++
			result.TotalBytes += file.Bytes
			if protected || id == stamp.BackendID && !file.Pending && used[file.Hash] {
				result.ReferencedCount++
				result.ReferencedBytes += file.Bytes
			} else {
				result.CleanableCount++
				result.CleanableBytes += file.Bytes
			}
		}
	}
	result.ScannedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return result, nil
}

// One user-facing unreferenced category includes inactive physical copies and
// incomplete uploads. Identity rows are not removed when a retired copy is.
func (s *Store) CleanResources(ctx context.Context, progress func(int, int64)) (int, int64, error) {
	control := s.ResourceControl()
	ids, err := control.BackendIDs(ctx)
	if err != nil {
		return 0, 0, err
	}
	var count int
	var freed int64
	for _, id := range ids {
		files, err := s.library.BackendFiles(ctx, id, true)
		if err != nil {
			return count, freed, err
		}
		for _, file := range files {
			release, err := s.library.Lock(ctx)
			if err != nil {
				return count, freed, err
			}
			removed := false
			err = func() error {
				allowed := false
				var stamp resources.Stamp
				var used bool
				err := s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					// Serialize with delayed ownership commits, but never hold
					// this transaction across the remote physical deletion.
					if err := tx.Model(&resources.RuntimeState{}).Where("id = ?", 1).Update("epoch", gorm.Expr("epoch")).Error; err != nil {
						return err
					}
					protected, err := (resources.Control{DB: tx, Library: s.library}).Protected(ctx, id)
					if err != nil || protected {
						return err
					}
					stamp, err = resources.ReadStamp(ctx, tx)
					if err != nil {
						return err
					}
					if file.Hash != "" {
						if err = tx.WithContext(ctx).Raw("SELECT "+resourceReferenced, file.Hash, file.Hash, file.Hash, file.Hash).Row().Scan(&used); err != nil {
							return err
						}
					}
					if id == stamp.BackendID && !file.Pending && used {
						return nil
					}
					allowed = true
					if file.Hash != "" && !file.Pending {
						return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&resources.Deletion{BackendID: id, Hash: file.Hash, Identity: file.ID}).Error
					}
					return nil
				})
				if err != nil || !allowed {
					return err
				}
				err = s.library.RemoveFile(ctx, file)
				if err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
				removed = err == nil
				return s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					if id == stamp.BackendID && file.Hash != "" && !used {
						if _, err := execSQL(ctx, tx, "DELETE FROM resources WHERE hash=? AND NOT ("+resourceReferenced+")", file.Hash, file.Hash, file.Hash, file.Hash, file.Hash); err != nil {
							return err
						}
					}
					if file.Hash != "" && !file.Pending {
						return tx.Where("backend_id = ? AND hash = ? AND identity = ?", id, file.Hash, file.ID).Delete(&resources.Deletion{}).Error
					}
					return nil
				})
			}()
			release()
			if err != nil {
				return count, freed, err
			}
			if removed {
				count++
				freed += file.Bytes
				if progress != nil {
					progress(count, freed)
				}
			}
		}
	}
	return count, freed, nil
}
