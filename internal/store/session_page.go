package store

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"

	sqlite "github.com/glebarez/go-sqlite"
	"gorm.io/gorm"
)

func init() {
	// SQLite's built-in lower/LIKE only fold ASCII. Preserve Unicode title
	// search when moving filtering from the browser into the database.
	sqlite.MustRegisterDeterministicScalarFunction("vista_contains", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		text, _ := args[0].(string)
		search, _ := args[1].(string)
		return strings.Contains(strings.ToLower(text), strings.ToLower(search)), nil
	})
}

type SessionPage struct {
	Pagination
	Items []Session `json:"items"`
}

type SessionSearch struct {
	Text          string
	UntitledLabel string
	Sort          SessionSort
}

type SessionSort string

const (
	SessionCreatedNewest SessionSort = "created-desc"
	SessionCreatedOldest SessionSort = "created-asc"
	SessionUpdatedNewest SessionSort = "updated-desc"
	SessionUpdatedOldest SessionSort = "updated-asc"
)

func (sort SessionSort) Valid() bool {
	switch sort {
	case "", SessionCreatedNewest, SessionCreatedOldest, SessionUpdatedNewest, SessionUpdatedOldest:
		return true
	}
	return false
}

func (sort SessionSort) byUpdate() bool {
	return sort == SessionUpdatedNewest || sort == SessionUpdatedOldest
}

func (sort SessionSort) order() string {
	direction := "DESC"
	if sort == SessionCreatedOldest || sort == SessionUpdatedOldest {
		direction = "ASC"
	}
	order := "sessions.pinned DESC, "
	if sort.byUpdate() {
		// Persisted events have fractional seconds, while creation times may not.
		// Parse both as dates rather than comparing their different text formats.
		order += "julianday(updated_at) " + direction + ", "
	}
	return order + "sessions.created_at " + direction + ", sessions.id " + direction
}

func (s *Store) SessionPage(ctx context.Context, page Pagination, search SessionSearch, open ...bool) (SessionPage, error) {
	if !search.Sort.Valid() {
		return SessionPage{}, fmt.Errorf("invalid session sort: %q", search.Sort)
	}
	result := SessionPage{}
	err := s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Table("sessions")
		if len(open) > 0 {
			query = query.Where("sessions.is_open = ?", open[0])
		}
		if text := strings.TrimSpace(search.Text); text != "" {
			if search.UntitledLabel == "" {
				query = query.Where("vista_contains(title, ?)", text)
			} else {
				query = query.Where("vista_contains(CASE WHEN title='New session' THEN ? ELSE title END, ?)", search.UntitledLabel, text)
			}
		}
		var total int64
		if err := query.Count(&total).Error; err != nil {
			return err
		}
		result.Pagination = page.WithTotal(int(total))
		var err error
		result.Items, err = readSessions(tx, query, result.PageSize, result.offset(), search.Sort)
		return err
	})
	return result, err
}
