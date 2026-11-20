package resources

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/willvar/dofs"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrMigrationBusy = errors.New("resource storage is already being changed")

type Migration struct {
	ID          string `gorm:"primaryKey" json:"id"`
	Source      string `json:"-"`
	Target      string `json:"-"`
	SourceEpoch int64  `json:"-"`
	Phase       string `json:"phase"`
	Error       string `json:"error,omitempty"`
	CopiedCount int    `json:"copiedCount"`
	CopiedBytes int64  `json:"copiedBytes"`
	CreatedAt   string `json:"-"`
}

func (Migration) TableName() string { return "resource_storage_migrations" }
func (m Migration) Protected() bool {
	return m.Phase == "syncing" || m.Phase == "failed" || m.Phase == "cancelling"
}

type copyRecord struct {
	MigrationID string `gorm:"primaryKey"`
	Hash        string `gorm:"primaryKey"`
	Bytes       int64
	Locator     string
	Verified    bool
}

func (copyRecord) TableName() string { return "resource_storage_copies" }

// Deletion fences delayed publication frames without holding an SQL
// transaction over physical deletion. Failed deletes retain their fence.
type Deletion struct {
	BackendID string `gorm:"primaryKey"`
	Hash      string `gorm:"primaryKey"`
	Identity  string
}

func (Deletion) TableName() string { return "resource_storage_deletions" }

type Control struct {
	DB      *gorm.DB
	Library Library
}

func InitSchema(db *gorm.DB) error {
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS resource_storage_state (id INTEGER PRIMARY KEY CHECK(id=1), backend_id TEXT NOT NULL, epoch INTEGER NOT NULL CHECK(epoch>0))`,
		`INSERT INTO resource_storage_state(id,backend_id,epoch) VALUES(1,'local',1) ON CONFLICT(id) DO NOTHING`,
		`CREATE TABLE IF NOT EXISTS resource_storage_migrations(id TEXT PRIMARY KEY,source TEXT NOT NULL,target TEXT NOT NULL,source_epoch INTEGER NOT NULL,phase TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',copied_count INTEGER NOT NULL DEFAULT 0,copied_bytes INTEGER NOT NULL DEFAULT 0,created_at TEXT NOT NULL)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS one_resource_migration ON resource_storage_migrations((1)) WHERE phase IN ('syncing','failed','cancelling')`,
		`CREATE TABLE IF NOT EXISTS resource_storage_deletions(backend_id TEXT NOT NULL,hash TEXT NOT NULL,identity TEXT NOT NULL,PRIMARY KEY(backend_id,hash))`,
		`CREATE TABLE IF NOT EXISTS resource_storage_copies(migration_id TEXT NOT NULL REFERENCES resource_storage_migrations(id) ON DELETE CASCADE,hash TEXT NOT NULL,bytes INTEGER NOT NULL,locator TEXT NOT NULL DEFAULT '',verified INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(migration_id,hash))`,
	} {
		if err := db.Exec(query).Error; err != nil {
			return err
		}
	}
	return nil
}
func (c Control) Current(ctx context.Context) (Stamp, error) { return ReadStamp(ctx, c.DB) }
func (c Control) Migration(ctx context.Context) (*Migration, error) {
	var job Migration
	err := c.DB.WithContext(ctx).Order("CASE WHEN phase IN ('syncing','failed','cancelling') THEN 0 ELSE 1 END").Order("created_at DESC, id DESC").Take(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &job, err
}
func (c Control) Start(ctx context.Context, target string, expected Stamp) (*Migration, error) {
	guard, err := c.Library.Lock(ctx)
	if err != nil {
		return nil, err
	}
	defer guard()
	settings, err := LoadSettings(c.Library.ConfigDir())
	if err != nil {
		return nil, err
	}
	if _, err = settings.Backend(target); err != nil {
		return nil, err
	}
	job := Migration{ID: uuid.NewString(), Source: expected.BackendID, Target: target, SourceEpoch: expected.Epoch, Phase: "syncing", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	err = c.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := CheckStamp(ctx, tx, expected); err != nil {
			return err
		}
		previous, err := (Control{DB: tx, Library: c.Library}).Migration(ctx)
		if err != nil {
			return err
		}
		if previous != nil && previous.Protected() {
			return ErrMigrationBusy
		}
		if expected.BackendID == target {
			return errors.New("resource storage is unchanged")
		}
		return tx.Create(&job).Error
	})
	return &job, err
}
func (c Control) Cancel(ctx context.Context, id string) error {
	// Competes with cutover in SQLite, without waiting for network I/O or ACK.
	result := c.DB.WithContext(ctx).Model(&Migration{}).Where("id = ? AND phase IN ?", id, []string{"syncing", "failed"}).Update("phase", "cancelling")
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("storage change has already completed or been cancelled")
	}
	return nil
}
func (c Control) Retry(ctx context.Context, id string) error {
	result := c.DB.WithContext(ctx).Model(&Migration{}).Where("id = ? AND phase = ?", id, "failed").Updates(map[string]any{"phase": "syncing", "error": ""})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrMigrationBusy
	}
	return nil
}
func (c Control) FinishCancellation(ctx context.Context, id string) error {
	// Caller has joined every copier before releasing task deletion protection.
	return c.DB.WithContext(ctx).Model(&Migration{}).Where("id = ? AND phase = ?", id, "cancelling").Updates(map[string]any{"phase": "cancelled", "error": ""}).Error
}
func (c Control) Fail(ctx context.Context, id string) error {
	return c.DB.WithContext(ctx).Model(&Migration{}).Where("id = ? AND phase = ?", id, "syncing").Updates(map[string]any{"phase": "failed", "error": "Resource transfer or verification failed. Retry the storage change."}).Error
}
func (c Control) Verified(ctx context.Context, id string) (map[string]copyRecord, error) {
	var items []copyRecord
	err := c.DB.WithContext(ctx).Where("migration_id = ? AND verified = ?", id, true).Find(&items).Error
	result := make(map[string]copyRecord, len(items))
	for _, item := range items {
		result[item.Hash] = item
	}
	return result, err
}
func (c Control) Copy(ctx context.Context, job Migration, f File) error {
	record := copyRecord{MigrationID: job.ID, Hash: f.Hash, Bytes: f.Bytes}
	if err := c.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record).Error; err != nil {
		return err
	}
	locator, err := c.Library.CopyOriginal(ctx, job.Source, job.Target, f.Hash, f.Bytes)
	if err != nil {
		return err
	}
	return c.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current Migration
		if err := tx.Take(&current, "id = ?", job.ID).Error; err != nil {
			return err
		}
		if current.Phase != "syncing" {
			return context.Canceled
		}
		if err := tx.Where("backend_id = ? AND hash = ?", job.Target, f.Hash).Delete(&Deletion{}).Error; err != nil {
			return err
		}
		if err := tx.Model(&copyRecord{}).Where("migration_id = ? AND hash = ?", job.ID, f.Hash).Updates(map[string]any{"verified": true, "locator": locator, "bytes": f.Bytes}).Error; err != nil {
			return err
		}
		var count int64
		var bytes int64
		if err := tx.Model(&copyRecord{}).Where("migration_id = ? AND verified = ?", job.ID, true).Count(&count).Error; err != nil {
			return err
		}
		if err := tx.Model(&copyRecord{}).Select("COALESCE(SUM(bytes),0)").Where("migration_id = ? AND verified = ?", job.ID, true).Scan(&bytes).Error; err != nil {
			return err
		}
		return tx.Model(&Migration{}).Where("id = ?", job.ID).Updates(map[string]any{"copied_count": count, "copied_bytes": bytes}).Error
	})
}
func missingFiles(files []File, verified map[string]copyRecord) []File {
	var missing []File
	for _, f := range files {
		if f.Hash != "" {
			v, ok := verified[f.Hash]
			if !ok || v.Bytes != f.Bytes {
				missing = append(missing, f)
			}
		}
	}
	return missing
}
func (c Control) Missing(ctx context.Context, job Migration) ([]File, error) {
	files, err := c.Library.BackendFiles(ctx, job.Source, false)
	if err != nil {
		return nil, err
	}
	// A missing retained original must not disappear from the migration proof.
	// Physical inventory also includes published orphans; retained references
	// add the completeness requirement even if a source file was lost.
	var required []struct {
		Hash  string
		Bytes int64
	}
	if err = c.DB.WithContext(ctx).Raw(`SELECT r.hash,r.bytes FROM resources r WHERE EXISTS(SELECT 1 FROM session_resources s WHERE s.hash=r.hash) OR EXISTS(SELECT 1 FROM message_resources m WHERE m.hash=r.hash) OR EXISTS(SELECT 1 FROM queued_input_resources q WHERE q.hash=r.hash) OR EXISTS(SELECT 1 FROM draft_resources d WHERE d.hash=r.hash)`).Scan(&required).Error; err != nil {
		return nil, err
	}
	present := map[string]int64{}
	for _, file := range files {
		present[file.Hash] = file.Bytes
	}
	for _, ref := range required {
		if size, ok := present[ref.Hash]; ok {
			if size != ref.Bytes {
				return nil, errors.New("source original size disagrees with its retained reference")
			}
		} else {
			files = append(files, File{Hash: ref.Hash, Bytes: ref.Bytes, BackendID: job.Source})
		}
	}
	verified, err := c.Verified(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	return missingFiles(files, verified), nil
}

// Cutover's only filesystem work is local directory/DOFS metadata enumeration.
// No S3 operation or content hashing occurs under the publication lock here.
func (c Control) Cutover(ctx context.Context, job Migration) (bool, error) {
	release, err := c.Library.Lock(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	missing, err := c.Missing(ctx, job)
	if err != nil || len(missing) > 0 {
		return false, err
	}
	switched := false
	err = c.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		expected := Stamp{BackendID: job.Source, Epoch: job.SourceEpoch}
		if err := CheckStamp(ctx, tx, expected); err != nil {
			return err
		}
		result := tx.Model(&Migration{}).Where("id = ? AND phase = ?", job.ID, "syncing").Update("phase", "complete")
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return context.Canceled
		}
		result = tx.Model(&RuntimeState{}).Where("id = ? AND backend_id = ? AND epoch = ?", 1, job.Source, job.SourceEpoch).Updates(map[string]any{"backend_id": job.Target, "epoch": job.SourceEpoch + 1})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrStorageChanged
		}
		switched = true
		return nil
	})
	return switched, err
}

// Reverify rebuilds target proofs after restart. A verified marker never hides
// target loss between process lifetimes or a changed physical generation.
func (c Control) Reverify(ctx context.Context, job Migration) error {
	verified, err := c.Verified(ctx, job.ID)
	if err != nil {
		return err
	}
	backend, err := c.Library.OpenBackend(ctx, job.Target)
	if err != nil {
		return err
	}
	defer backend.Close()
	for hash, item := range verified {
		r, size, locator, e := backend.Open(ctx, hash)
		if e == nil {
			e = verifyReader(ctx, r, hash, item.Bytes)
			e = errors.Join(e, r.Close())
		}
		if e != nil || size != item.Bytes || locator != item.Locator {
			if err = c.DB.WithContext(ctx).Model(&copyRecord{}).Where("migration_id = ? AND hash = ?", job.ID, hash).Update("verified", false).Error; err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

func (c Control) BackendIDs(ctx context.Context) ([]string, error) {
	settings, err := LoadSettings(c.Library.ConfigDir())
	if err != nil {
		return nil, err
	}
	ids := []string{LocalBackend}
	for _, backend := range settings.Backends {
		ids = append(ids, backend.ID)
	}
	slices.Sort(ids)
	return ids, nil
}
func (c Control) Protected(ctx context.Context, id string) (bool, error) {
	if busy, err := c.Library.BackendBusy(id); err != nil || busy {
		return busy, err
	}
	var count int64
	err := c.DB.WithContext(ctx).Model(&Migration{}).Where("phase IN ? AND (source = ? OR target = ?)", []string{"syncing", "failed", "cancelling"}, id, id).Count(&count).Error
	return count > 0, err
}

// Prepare initializes only this instance's exact namespace; it never changes
// bucket policies or grants. The caller persists the backend before any payload.
func Prepare(ctx context.Context, stateDir string, cfg BackendConfig) error {
	release, err := (Library{StateDir: stateDir}).lockBackendCheck(ctx, cfg.ID)
	if err != nil {
		return err
	}
	defer release()
	backend, err := openObjectBackend(ctx, stateDir, cfg, true)
	if err != nil {
		return err
	}
	defer backend.Close()
	return nil
}
func CheckBackend(ctx context.Context, stateDir string, cfg BackendConfig) error {
	if cfg.Kind == "local" {
		return nil
	}
	release, err := (Library{StateDir: stateDir}).lockBackendCheck(ctx, cfg.ID)
	if err != nil {
		return err
	}
	defer release()
	b, err := openObjectBackend(ctx, stateDir, cfg, false)
	if err != nil {
		return err
	}
	defer b.Close()
	if checker, ok := b.objects.(interface {
		VersioningStatus(context.Context) (string, error)
	}); ok {
		status, err := checker.VersioningStatus(ctx)
		if err != nil {
			return err
		}
		if status != "" {
			return errors.New("resource storage requires a bucket without object versioning")
		}
	}
	if _, err = b.Files(ctx, true); err != nil {
		return err
	}
	// A raw encrypted check object is not a published resource. Its intent is
	// enumerable even if the check is interrupted before deletion.
	payload := []byte("Vista resource storage verification " + uuid.NewString())
	hash := sha256.Sum256(payload)
	id := uuid.NewString()
	root, _ := dofs.ObjectRoot(cfg.ID)
	key := root + "checks/" + id
	if err = saveIntent(b.intentPath(id), objectIntent{UploadID: id, ObjectKey: key, Bytes: int64(len(payload))}); err != nil {
		return err
	}
	fileKey, err := dofs.GenerateKey()
	if err != nil {
		return err
	}
	defer dofs.Clear(fileKey)
	var ciphertext bytes.Buffer
	if err = dofs.EncryptStream(fileKey, bytes.NewReader(payload), &ciphertext); err != nil {
		return err
	}
	encrypted := ciphertext.Bytes()
	if err = b.objects.Put(ctx, key, bytes.NewReader(encrypted), int64(len(encrypted))); err != nil {
		return err
	}
	r, err := b.objects.GetRange(ctx, key, 0, int64(len(encrypted))-1)
	if err != nil {
		return err
	}
	var plaintext bytes.Buffer
	err = dofs.DecryptStream(fileKey, r, &plaintext)
	closeErr := r.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	plain := plaintext.Bytes()
	if sum := sha256.Sum256(plain); hex.EncodeToString(sum[:]) != hex.EncodeToString(hash[:]) {
		return errors.New("object storage verification mismatch")
	}
	if err = b.objects.Delete(ctx, key); err != nil {
		return err
	}
	if _, err = b.objects.Stat(ctx, key); !errors.Is(err, dofs.ErrObjectNotFound) {
		return errors.New("object storage deletion was not confirmed")
	}
	return os.Remove(b.intentPath(id))
}
