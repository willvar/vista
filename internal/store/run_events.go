package store

import (
	"context"
	"database/sql"
	"errors"

	"gorm.io/gorm"
)

const runTurnEventPageSize = 100

var ErrRunEventRange = errors.New("invalid execution event range")

func requireRunOwnership(tx *gorm.DB, sessionID, runID string) error {
	var count int64
	if err := tx.Table("runs").Where("session_id=? AND id=?", sessionID, runID).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RequireRun(ctx context.Context, sessionID, runID string) error {
	return requireRunOwnership(s.orm.WithContext(ctx), sessionID, runID)
}

// RunEvent reads the original event, rather than its compact overview preview.
func (s *Store) RunEvent(ctx context.Context, sessionID, runID string, sequence int64) (Event, error) {
	if sequence <= 0 {
		return Event{}, ErrRunEventRange
	}
	var event Event
	err := s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := requireRunOwnership(tx, sessionID, runID); err != nil {
			return err
		}
		return tx.Table("events").Select("session_id,sequence,kind,CAST(data AS BLOB) AS data,created_at").
			Where("session_id=? AND sequence=? AND json_extract(data,'$.runId')=?", sessionID, sequence, runID).Take(&event).Error
	}, &sql.TxOptions{ReadOnly: true})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ErrNotFound
	}
	return event, err
}

// RunTurnEvents returns the latest bounded page, in chronological order. Before
// is exclusive; zero selects the latest page without traversing other turns.
func (s *Store) RunTurnEvents(ctx context.Context, sessionID, runID string, turn int32, before int64) ([]Event, error) {
	if turn <= 0 || before < 0 {
		return nil, ErrRunEventRange
	}
	events := []Event{}
	err := s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := requireRunOwnership(tx, sessionID, runID); err != nil {
			return err
		}
		query := tx.Table("events").Select("session_id,sequence,kind,CAST(data AS BLOB) AS data,created_at").
			Where("session_id=? AND json_extract(data,'$.runId')=? AND json_extract(data,'$.turn')=?", sessionID, runID, turn)
		if before > 0 {
			query = query.Where("sequence<?", before)
		}
		return query.Order("sequence DESC").Limit(runTurnEventPageSize).Find(&events).Error
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
		events[left], events[right] = events[right], events[left]
	}
	return events, nil
}
