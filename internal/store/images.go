package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"vista/internal/resources"
	"vista/internal/workerproto"

	"gorm.io/gorm"
)

const MaxImageBytes = resources.MaxImageBytes

func ValidateImages(images []Image) error {
	for _, image := range images {
		if len(image.Data) == 0 && resources.ValidHash(image.Hash) && image.Bytes > 0 && image.Bytes <= MaxImageBytes && resources.ValidImageType(image.MediaType) && len(image.Name) <= 1024 {
			continue
		}
		if len(image.Data) == 0 || len(image.Data) > MaxImageBytes {
			return errors.New("image must be 1 byte to 50 MiB")
		}
		switch image.MediaType {
		case "image/png", "image/jpeg", "image/webp", "image/gif":
		default:
			return errors.New("unsupported image media type")
		}
		if http.DetectContentType(image.Data) != image.MediaType {
			return errors.New("image media type does not match content")
		}
	}
	return nil
}

func (image Image) Ref() resources.Ref {
	return resources.Ref{Hash: image.Hash, MediaType: image.MediaType, Bytes: image.Bytes, Name: image.Name}
}

func (s *Store) lockImages(ctx context.Context, images []Image) (func(), error) {
	if len(images) == 0 {
		return func() {}, nil
	}
	return s.library.Lock(ctx)
}

func (s *Store) writeImages(ctx context.Context, tx *gorm.DB, sessionID, table, column, id string, images []Image) error {
	if err := ValidateImages(images); err != nil {
		return err
	}
	for position, image := range images {
		hash, size := image.Hash, image.Bytes
		if len(image.Data) > 0 {
			if !resources.ValidHash(hash) || size != int64(len(image.Data)) {
				return errors.New("image was not published before its reference transaction")
			}
		} else {
			var exists int
			err := tx.WithContext(ctx).Raw("SELECT count(*) FROM resources r JOIN session_resources sr ON sr.hash=r.hash WHERE sr.session_id=? AND r.hash=? AND r.bytes=? AND r.media_type=?", sessionID, hash, size, image.MediaType).Row().Scan(&exists)
			if err != nil {
				return err
			}
			if exists != 1 {
				return errors.New("image resource is not owned by this Session")
			}
		}
		if _, err := execSQL(ctx, tx, "INSERT INTO resources(hash,media_type,bytes,created_at) VALUES(?,?,?,?) ON CONFLICT(hash) DO NOTHING", hash, image.MediaType, size, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if _, err := execSQL(ctx, tx, "INSERT INTO session_resources(session_id,hash,name) VALUES(?,?,?) ON CONFLICT DO NOTHING", sessionID, hash, image.Name); err != nil {
			return err
		}
		if _, err := execSQL(ctx, tx, "INSERT INTO "+table+"("+column+",position,hash,name) VALUES(?,?,?,?)", id, position, hash, image.Name); err != nil {
			return err
		}
	}
	return nil
}

func readResourceRows(rows *sql.Rows) ([]resources.Ref, error) {
	defer rows.Close()
	result := []resources.Ref{}
	for rows.Next() {
		var ref resources.Ref
		if err := rows.Scan(&ref.Hash, &ref.MediaType, &ref.Bytes, &ref.Name); err != nil {
			return nil, err
		}
		result = append(result, ref)
	}
	return result, rows.Err()
}

func (s *Store) MessageResources(ctx context.Context, sessionID, messageID string) ([]resources.Ref, error) {
	rows, err := s.orm.WithContext(ctx).Raw(`SELECT r.hash,r.media_type,r.bytes,i.name FROM message_resources i
 JOIN resources r ON r.hash=i.hash JOIN history_messages m ON m.id=i.message_id
 WHERE m.session_id=? AND m.id=? ORDER BY i.position`, sessionID, messageID).Rows()
	if err != nil {
		return nil, err
	}
	return readResourceRows(rows)
}

func (s *Store) ResolveImages(ctx context.Context, refs []resources.Ref) ([]Image, error) {
	if len(refs) == 0 {
		return []Image{}, nil
	}
	unlock, err := s.library.Lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	images := make([]Image, 0, len(refs))
	for _, ref := range refs {
		if ref.Bytes > MaxImageBytes {
			return nil, errors.New("image exceeds 50 MiB")
		}
		data, err := s.library.ReadContext(ctx, ref)
		if err != nil {
			return nil, err
		}
		images = append(images, Image{Hash: ref.Hash, MediaType: ref.MediaType, Bytes: ref.Bytes, Name: ref.Name, Data: data})
	}
	return images, nil
}

func (s *Store) MessageImages(ctx context.Context, sessionID, messageID string) ([]Image, error) {
	refs, err := s.MessageResources(ctx, sessionID, messageID)
	if err != nil {
		return nil, err
	}
	return s.ResolveImages(ctx, refs)
}

func (s *Store) MessageImage(ctx context.Context, sessionID, messageID string, position int) (Image, error) {
	refs, err := s.MessageResources(ctx, sessionID, messageID)
	if err != nil {
		return Image{}, err
	}
	if position < 0 || position >= len(refs) {
		return Image{}, ErrNotFound
	}
	images, err := s.ResolveImages(ctx, refs[position:position+1])
	if err != nil {
		return Image{}, err
	}
	return images[0], nil
}

func (s *Store) SessionResource(ctx context.Context, sessionID, hash string) (resources.Ref, error) {
	var ref resources.Ref
	err := s.orm.WithContext(ctx).Raw(`SELECT r.hash,r.media_type,r.bytes,COALESCE((SELECT i.name FROM message_resources i JOIN history_messages m ON m.id=i.message_id WHERE m.session_id=? AND i.hash=r.hash ORDER BY m.sequence DESC LIMIT 1),sr.name)
 FROM resources r JOIN session_resources sr ON sr.hash=r.hash WHERE sr.session_id=? AND r.hash=?`, sessionID, sessionID, hash).Row().Scan(&ref.Hash, &ref.MediaType, &ref.Bytes, &ref.Name)
	return ref, err
}

func (s *Store) SessionResources(ctx context.Context, sessionID string, after string, limit int) ([]resources.Ref, error) {
	rows, err := s.orm.WithContext(ctx).Raw(`SELECT r.hash,r.media_type,r.bytes,COALESCE((SELECT i.name FROM message_resources i JOIN history_messages m ON m.id=i.message_id WHERE m.session_id=? AND i.hash=r.hash ORDER BY m.sequence DESC LIMIT 1),sr.name)
 FROM resources r JOIN session_resources sr ON sr.hash=r.hash WHERE sr.session_id=? AND (?='' OR r.hash>?) ORDER BY r.hash LIMIT ?`, sessionID, sessionID, after, after, limit).Rows()
	if err != nil {
		return nil, err
	}
	return readResourceRows(rows)
}

// AttachResource is called only for Worker-verified originals while that Worker
// holds the library lock. Workers publish bytes; Server alone commits ownership.
// A tool source records the history boundary that retains an imported/reread
// original across later rollbacks. Unsent HTTP uploads have no tool source.
func (s *Store) AttachResource(ctx context.Context, sessionID string, ref resources.Ref, source *workerproto.Result, stamps ...resources.Stamp) error {
	if !resources.ValidHash(ref.Hash) || !resources.ValidResourceSize(ref.MediaType, ref.Bytes) || len(ref.Name) > 1024 {
		return errors.New("invalid resource reference")
	}
	stamp, err := s.ResourceControl().Current(ctx)
	if err != nil {
		return err
	}
	if len(stamps) > 0 {
		stamp = stamps[0]
	}
	tx, err := s.begin(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = resources.CheckStamp(ctx, tx, stamp); err != nil {
		return err
	}
	var deleting int64
	if err = tx.WithContext(ctx).Model(&resources.Deletion{}).Where("backend_id = ? AND hash = ?", stamp.BackendID, ref.Hash).Count(&deleting).Error; err != nil {
		return err
	}
	if deleting != 0 {
		return errors.New("resource cleanup is pending; retry after cleanup completes")
	}
	// Local filesystem/DOFS metadata only; no remote request in this transaction.
	if err = s.library.Stat(ctx, stamp, ref); err != nil {
		return err
	}

	var open bool
	if err = tx.WithContext(ctx).Raw("SELECT is_open FROM sessions WHERE id=?", sessionID).Row().Scan(&open); err != nil {
		return err
	}
	if !open {
		return ErrClosed
	}
	if source != nil {
		active, err := executionActive(ctx, tx, sessionID, source.RunID, source.TaskID)
		if err != nil {
			return err
		}
		if !active {
			return ErrNotFound
		}
	}
	if _, err = execSQL(ctx, tx, "INSERT INTO resources(hash,media_type,bytes,created_at) VALUES(?,?,?,?) ON CONFLICT(hash) DO NOTHING", ref.Hash, ref.MediaType, ref.Bytes, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err = execSQL(ctx, tx, "INSERT INTO session_resources(session_id,hash,name) VALUES(?,?,?) ON CONFLICT DO NOTHING", sessionID, ref.Hash, ref.Name); err != nil {
		return err
	}
	if source != nil {
		if _, err = appendEvent(ctx, tx, sessionID, "resource_import", map[string]any{"runId": source.RunID, "taskId": source.TaskID, "resource": ref}); err != nil {
			return err
		}
	}
	return tx.Commit().Error
}

// Publication happens before SQL reference transactions. A failed reference
// commit leaves an explicitly cleanable immutable original, never SQL-held I/O.
func (s *Store) prepareImages(ctx context.Context, images []Image) ([]Image, error) {
	prepared := append([]Image(nil), images...)
	for i, image := range prepared {
		if len(image.Data) > 0 {
			hash, size, err := s.library.Put(ctx, bytes.NewReader(image.Data))
			if err != nil {
				return nil, err
			}
			prepared[i].Hash, prepared[i].Bytes = hash, size
		}
	}
	return prepared, nil
}
func (s *Store) ResourceControl() resources.Control {
	return resources.Control{DB: s.orm, Library: s.library}
}
