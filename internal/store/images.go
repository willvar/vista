package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"vista/internal/resources"
)

const MaxImageBytes = 1 << 20

func ValidateImages(images []Image) error {
	for _, image := range images {
		if len(image.Data) == 0 || len(image.Data) > MaxImageBytes {
			return errors.New("image must be 1 byte to 1 MiB")
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

func (s *Store) writeImages(ctx context.Context, tx *sql.Tx, sessionID, table, column, id string, images []Image) error {
	if err := ValidateImages(images); err != nil {
		return err
	}
	for position, image := range images {
		hash, size, err := s.library.Put(ctx, bytes.NewReader(image.Data))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO resources(hash,media_type,bytes,created_at) VALUES(?,?,?,?) ON CONFLICT(hash) DO NOTHING", hash, image.MediaType, size, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO session_resources(session_id,hash,name) VALUES(?,?,?) ON CONFLICT DO NOTHING", sessionID, hash, image.Name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO "+table+"("+column+",position,hash,name) VALUES(?,?,?,?)", id, position, hash, image.Name); err != nil {
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
	rows, err := s.db.QueryContext(ctx, `SELECT r.hash,r.media_type,r.bytes,i.name FROM message_resources i
 JOIN resources r ON r.hash=i.hash JOIN messages m ON m.id=i.message_id
 WHERE m.session_id=? AND m.id=? ORDER BY i.position`, sessionID, messageID)
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
			return nil, errors.New("image exceeds 1 MiB")
		}
		data, err := s.library.Read(ref)
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
		return Image{}, sql.ErrNoRows
	}
	images, err := s.ResolveImages(ctx, refs[position:position+1])
	if err != nil {
		return Image{}, err
	}
	return images[0], nil
}

func (s *Store) SessionResource(ctx context.Context, sessionID, hash string) (resources.Ref, error) {
	var ref resources.Ref
	err := s.db.QueryRowContext(ctx, `SELECT r.hash,r.media_type,r.bytes,COALESCE((SELECT i.name FROM message_resources i JOIN messages m ON m.id=i.message_id WHERE m.session_id=? AND i.hash=r.hash ORDER BY m.sequence DESC LIMIT 1),sr.name)
 FROM resources r JOIN session_resources sr ON sr.hash=r.hash WHERE sr.session_id=? AND r.hash=?`, sessionID, sessionID, hash).Scan(&ref.Hash, &ref.MediaType, &ref.Bytes, &ref.Name)
	return ref, err
}

func (s *Store) SessionResources(ctx context.Context, sessionID string, after string, limit int) ([]resources.Ref, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.hash,r.media_type,r.bytes,COALESCE((SELECT i.name FROM message_resources i JOIN messages m ON m.id=i.message_id WHERE m.session_id=? AND i.hash=r.hash ORDER BY m.sequence DESC LIMIT 1),sr.name)
 FROM resources r JOIN session_resources sr ON sr.hash=r.hash WHERE sr.session_id=? AND (?='' OR r.hash>?) ORDER BY r.hash LIMIT ?`, sessionID, sessionID, after, after, limit)
	if err != nil {
		return nil, err
	}
	return readResourceRows(rows)
}
