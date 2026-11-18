package resources

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	_ "modernc.org/sqlite"
)

var ErrStorageChanged = errors.New("resource storage changed before publication was committed")

// Stamp belongs only to private publication IPC, never canonical resource refs.
type Stamp struct {
	BackendID string `json:"backendId"`
	Epoch     int64  `json:"epoch"`
}
type RuntimeState struct {
	ID        int    `gorm:"primaryKey" json:"-"`
	BackendID string `json:"backendId"`
	Epoch     int64  `json:"epoch"`
}

func (RuntimeState) TableName() string { return "resource_storage_state" }
func ReadStamp(ctx context.Context, db *gorm.DB) (Stamp, error) {
	var state RuntimeState
	if err := db.WithContext(ctx).Take(&state, "id = ?", 1).Error; err != nil {
		return Stamp{}, err
	}
	if state.BackendID == "" || state.Epoch < 1 {
		return Stamp{}, errors.New("invalid active resource storage state")
	}
	return Stamp{BackendID: state.BackendID, Epoch: state.Epoch}, nil
}

// Conditional write takes SQLite's writer position before ownership mutations.
// The Server must not acquire the Worker's resource lock in this ACK path.
func CheckStamp(ctx context.Context, tx *gorm.DB, stamp Stamp) error {
	result := tx.WithContext(ctx).Model(&RuntimeState{}).Where("id = ? AND backend_id = ? AND epoch = ?", 1, stamp.BackendID, stamp.Epoch).Update("epoch", gorm.Expr("epoch"))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrStorageChanged
	}
	return nil
}
func (l Library) Stamp(ctx context.Context) (Stamp, error) {
	path := filepath.Join(l.StateDir, "vista.db")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return Stamp{BackendID: LocalBackend, Epoch: 1}, nil
	} else if err != nil {
		return Stamp{}, err
	}
	query := url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout=5000", "query_only=ON"}}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Stamp{}, err
	}
	slash := filepath.ToSlash(absolute)
	if !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	dsn := (&url.URL{Scheme: "file", Path: slash, RawQuery: query.Encode()}).String()
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		return Stamp{}, err
	}
	defer raw.Close()
	raw.SetMaxOpenConns(1)
	db, err := gorm.Open(gormsqlite.Dialector{DriverName: "sqlite", Conn: raw}, &gorm.Config{DisableAutomaticPing: true, SkipDefaultTransaction: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return Stamp{}, err
	}
	return ReadStamp(ctx, db)
}
