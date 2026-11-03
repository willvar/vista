package store

import (
	"context"
	"database/sql/driver"
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
}

func (s *Store) SessionPage(ctx context.Context, page Pagination, search SessionSearch, open ...bool) (SessionPage, error) {
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
		result.Items, err = readSessions(tx, query, result.PageSize, result.offset())
		return err
	})
	return result, err
}
