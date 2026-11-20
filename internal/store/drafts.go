package store

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"gorm.io/gorm"
	"vista/internal/resources"
)

const HomeDraft = "home"

var ErrDraftConflict = errors.New("the draft has changed")
var ErrDraftImage = errors.New("invalid draft attachment")

type Draft struct {
	Revision int64           `json:"revision"`
	Text     string          `json:"text"`
	Quote    json.RawMessage `json:"quote,omitempty"`
	Images   []resources.Ref `json:"images"`
}

// DraftVersion identifies the precise draft accepted with a send. A newer
// revision is not consumed, even if it happens to have identical text.
type DraftVersion struct {
	Key      string `json:"key"`
	Revision int64  `json:"revision"`
}

func draftSession(key string) any {
	if key == HomeDraft {
		return nil
	}
	return key
}

func loadDraft(ctx context.Context, tx *gorm.DB, key string) (Draft, error) {
	d := Draft{Images: []resources.Ref{}}
	if key != HomeDraft {
		var exists bool
		if err := tx.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM sessions WHERE id=?)`, key).Row().Scan(&exists); err != nil {
			return d, err
		}
		if !exists {
			return d, ErrNotFound
		}
	}
	var quote []byte
	err := tx.WithContext(ctx).Raw(`SELECT revision,text,quote FROM drafts WHERE key=?`, key).Row().Scan(&d.Revision, &d.Text, &quote)
	d.Quote = quote
	if errors.Is(err, ErrNotFound) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	rows, err := tx.WithContext(ctx).Raw(`SELECT r.hash,r.media_type,r.bytes,i.name FROM draft_resources i JOIN resources r ON r.hash=i.hash WHERE i.draft_key=? ORDER BY i.position`, key).Rows()
	if err != nil {
		return d, err
	}
	d.Images, err = readResourceRows(rows)
	return d, err
}

func (s *Store) Draft(ctx context.Context, key string) (Draft, error) {
	var d Draft
	err := s.readView(ctx, func(view *Store) (err error) { d, err = loadDraft(ctx, view.orm, key); return err })
	return d, err
}

func (s *Store) SaveDraft(ctx context.Context, key string, d Draft) (Draft, error) {
	err := s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		before, err := loadDraft(ctx, tx, key)
		if err != nil {
			return err
		}
		if before.Revision != d.Revision {
			return ErrDraftConflict
		}
		for _, ref := range d.Images {
			owned, err := draftImageOwned(ctx, tx, key, key, ref)
			if err != nil {
				return err
			}
			if !owned {
				return ErrDraftImage
			}
		}
		return writeDraft(ctx, tx, key, &d)
	})
	return d, err
}

func writeDraft(ctx context.Context, tx *gorm.DB, key string, d *Draft) error {
	d.Revision++
	if d.Images == nil {
		d.Images = []resources.Ref{}
	}
	if _, err := execSQL(ctx, tx, `INSERT INTO drafts(key,session_id,revision,text,quote) VALUES(?,?,?,?,?)
 ON CONFLICT(key) DO UPDATE SET revision=excluded.revision,text=excluded.text,quote=excluded.quote`, key, draftSession(key), d.Revision, d.Text, []byte(d.Quote)); err != nil {
		return err
	}
	if _, err := execSQL(ctx, tx, `DELETE FROM draft_resources WHERE draft_key=?`, key); err != nil {
		return err
	}
	for i, ref := range d.Images {
		if _, err := execSQL(ctx, tx, `INSERT INTO draft_resources(draft_key,position,hash,name) VALUES(?,?,?,?)`, key, i, ref.Hash, ref.Name); err != nil {
			return err
		}
	}
	return nil
}

// This check is repeated in the transaction of a send. A successful GET is
// not a lease on a reference another tab may have removed in the meantime.
func draftImageOwned(ctx context.Context, tx *gorm.DB, sessionID, key string, ref resources.Ref) (bool, error) {
	var allowed bool
	err := tx.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM resources r WHERE r.hash=? AND r.bytes=? AND r.media_type=? AND (
  EXISTS(SELECT 1 FROM draft_resources WHERE draft_key=? AND hash=r.hash) OR EXISTS(SELECT 1 FROM session_resources WHERE session_id=? AND hash=r.hash)))`, ref.Hash, ref.Bytes, ref.MediaType, key, sessionID).Row().Scan(&allowed)
	return allowed, err
}

func (s *Store) DraftImage(ctx context.Context, key, hash string) (resources.Ref, error) {
	var ref resources.Ref
	err := s.orm.WithContext(ctx).Raw(`SELECT r.hash,r.media_type,r.bytes,i.name FROM draft_resources i JOIN resources r ON r.hash=i.hash WHERE i.draft_key=? AND i.hash=? LIMIT 1`, key, hash).Row().Scan(&ref.Hash, &ref.MediaType, &ref.Bytes, &ref.Name)
	return ref, err
}

func (s *Store) CanSendDraftImage(ctx context.Context, sessionID, key string, ref resources.Ref) (bool, error) {
	return draftImageOwned(ctx, s.orm, sessionID, key, ref)
}

func (s *Store) UploadDraftImage(ctx context.Context, key string, revision int64, name, mediaType string, input io.Reader) (Draft, error) {
	var d Draft
	r := bufio.NewReader(input)
	prefix, err := r.Peek(512)
	if err != nil && !errors.Is(err, io.EOF) {
		return d, err
	}
	if len(prefix) == 0 || !resources.ValidImageType(mediaType) || http.DetectContentType(prefix) != mediaType {
		return d, ErrDraftImage
	}
	release, err := s.library.Lock(ctx)
	if err != nil {
		return d, err
	}
	defer release()
	// Do not upload an obsolete request. The transaction rechecks the revision
	// after publishing the immutable original, without holding SQL over I/O.
	d, err = s.Draft(ctx, key)
	if err != nil {
		return d, err
	}
	if d.Revision != revision {
		return d, ErrDraftConflict
	}
	if len(d.Images) >= 32 {
		return d, ErrDraftImage
	}
	hash, bytes, err := s.library.Put(ctx, io.LimitReader(r, resources.MaxImageBytes+1))
	if err != nil {
		return d, err
	}
	if bytes > resources.MaxImageBytes {
		return d, ErrDraftImage
	}
	err = s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		d, err = loadDraft(ctx, tx, key)
		if err != nil {
			return err
		}
		if d.Revision != revision {
			return ErrDraftConflict
		}
		if _, err := execSQL(ctx, tx, `INSERT INTO resources(hash,media_type,bytes,created_at) VALUES(?,?,?,?) ON CONFLICT(hash) DO NOTHING`, hash, mediaType, bytes, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		d.Images = append(d.Images, resources.Ref{Hash: hash, Name: name, MediaType: mediaType, Bytes: bytes})
		return writeDraft(ctx, tx, key, &d)
	})
	return d, err
}

func draftKey(d *DraftVersion) string {
	if d == nil {
		return ""
	}
	return d.Key
}

func consumeDraft(ctx context.Context, tx *gorm.DB, version *DraftVersion) error {
	if version == nil {
		return nil
	}
	res, err := execSQL(ctx, tx, `UPDATE drafts SET text='',quote=NULL,revision=revision+1 WHERE key=? AND revision=?`, version.Key, version.Revision)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	_, err = execSQL(ctx, tx, `DELETE FROM draft_resources WHERE draft_key=?`, version.Key)
	return err
}
