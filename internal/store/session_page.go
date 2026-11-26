package store

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"unicode"

	"github.com/mozillazg/go-pinyin"
	"golang.org/x/text/unicode/norm"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	sqlite "modernc.org/sqlite"
)

func init() {
	// SQLite's built-in lower/LIKE only fold ASCII. Preserve Unicode title
	// search when moving filtering from the browser into the database.
	sqlite.MustRegisterDeterministicScalarFunction("vista_contains", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		text, _ := args[0].(string)
		search, _ := args[1].(string)
		return strings.Contains(strings.ToLower(text), strings.ToLower(search)), nil
	})
	sqlite.MustRegisterDeterministicScalarFunction("vista_title_key", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		title, _ := args[0].(string)
		return sessionTitleKey(title), nil
	})
}

func sessionTitleKey(title string) string {
	title = strings.ToLower(norm.NFKC.String(strings.TrimSpace(title)))
	var key strings.Builder
	key.Grow(len(title))
	// Use the dictionary's primary reading for polyphonic characters. This
	// keeps the order deterministic without carting a phrase model into SQLite.
	args := pinyin.Args{Style: pinyin.Normal}
	for _, r := range title {
		if unicode.Is(unicode.Han, r) {
			if reading := pinyin.SinglePinyin(r, args); len(reading) != 0 {
				key.WriteString(reading[0])
				continue
			}
		}
		key.WriteRune(r)
	}
	return key.String()
}

type SessionPage struct {
	Pagination
	Items            []Session        `json:"items"`
	Attention        *int             `json:"attention,omitempty"`
	ThroughSequences map[string]int64 `json:"throughSequences,omitempty"`
}

type SessionSearch struct {
	Text             string
	UntitledLabel    string
	Sort             SessionSort
	IncludeSequences bool
	AttentionWorkers []string // Non-nil requests the open conversation attention count.
}

type SessionSort string

const (
	SessionCreatedNewest SessionSort = "created-desc"
	SessionCreatedOldest SessionSort = "created-asc"
	SessionUpdatedNewest SessionSort = "updated-desc"
	SessionUpdatedOldest SessionSort = "updated-asc"
	SessionTitleAZ       SessionSort = "title-asc"
)

func (sort SessionSort) Valid() bool {
	switch sort {
	case "", SessionCreatedNewest, SessionCreatedOldest, SessionUpdatedNewest, SessionUpdatedOldest, SessionTitleAZ:
		return true
	}
	return false
}

func (sort SessionSort) byUpdate() bool {
	return sort == SessionUpdatedNewest || sort == SessionUpdatedOldest
}

func (search SessionSearch) order() clause.OrderBy {
	sort := search.Sort
	if sort == SessionTitleAZ {
		title := "sessions.title"
		var args []any
		if search.UntitledLabel != "" {
			title = `CASE WHEN sessions.title='New session' THEN ? ELSE sessions.title END`
			args = []any{search.UntitledLabel}
		}
		return clause.OrderBy{Expression: clause.Expr{
			SQL:  "sessions.pinned DESC, vista_title_key(" + title + "), CASE WHEN sessions.title='New session' THEN sessions.created_at END, sessions.id",
			Vars: args,
		}}
	}
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
	return clause.OrderBy{Expression: clause.Expr{SQL: order + "sessions.created_at " + direction + ", sessions.id " + direction}}
}

func (s *Store) SessionPage(ctx context.Context, page Pagination, search SessionSearch, open ...bool) (SessionPage, error) {
	if !search.Sort.Valid() {
		return SessionPage{}, fmt.Errorf("invalid session sort: %q", search.Sort)
	}
	result := SessionPage{}
	err := s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := sessionGroupQuery(tx, open)
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
		result.Items, err = readSessionGroups(tx, query, result.PageSize, result.offset(), search)
		if err != nil {
			return err
		}
		if search.IncludeSequences {
			if result.ThroughSequences, err = sessionBoundaries(tx, result.Items); err != nil {
				return err
			}
		}
		if search.AttentionWorkers != nil {
			result.Attention = new(int)
			return countSessionAttention(tx, search.AttentionWorkers, result.Attention)
		}
		return nil
	})
	return result, err
}

// Match the sidebar badges: pending replies, unread assistant messages and
// paused inputs may come from any open branch. Only roots have a worker badge.
func countSessionAttention(tx *gorm.DB, workers []string, count *int) error {
	missingWorker := "b.branch_id IS NULL"
	var args []any
	if len(workers) != 0 {
		missingWorker += " AND s.id NOT IN ?"
		args = []any{workers}
	}
	return tx.Raw(`SELECT COUNT(DISTINCT COALESCE(b.root_id,s.id))
 FROM sessions s LEFT JOIN conversation_branches b ON b.branch_id=s.id
 WHERE s.is_open=1 AND ((`+missingWorker+`) OR s.pending_kind<>'' OR EXISTS (
  SELECT 1 FROM messages m WHERE m.session_id=s.id AND m.sequence>s.read_sequence AND m.role='assistant'
    AND (NOT EXISTS(SELECT 1 FROM live_history_starts l WHERE l.session_id=s.id)
      OR EXISTS(SELECT 1 FROM history_messages h WHERE h.session_id=s.id AND h.id=m.id))
 ) OR (s.input_queue_paused=1 AND EXISTS (
  SELECT 1 FROM queued_inputs q WHERE q.session_id=s.id AND q.status IN ('queued','paused')
 )))`, args...).Row().Scan(count)
}
