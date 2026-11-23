package store

import (
	"context"

	"gorm.io/gorm"
)

// SessionListSnapshot binds list metadata to each conversation's saved event
// boundary. The list does not advance a client's durable conversation cursor.
type SessionListSnapshot struct {
	Items            []Session        `json:"items"`
	ThroughSequences map[string]int64 `json:"throughSequences"`
}

func (s *Store) ListSnapshot(ctx context.Context, open ...bool) (SessionListSnapshot, error) {
	var snapshot SessionListSnapshot
	err := s.readView(ctx, func(view *Store) (err error) {
		if snapshot.Items, err = view.list(ctx, open...); err != nil {
			return err
		}
		snapshot.ThroughSequences, err = sessionBoundaries(view.orm.WithContext(ctx), snapshot.Items)
		return err
	})
	return snapshot, err
}

func sessionBoundaries(db *gorm.DB, items []Session) (map[string]int64, error) {
	through := make(map[string]int64, len(items))
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
		through[item.ID] = 0
	}
	if len(ids) == 0 {
		return through, nil
	}
	var rows []struct {
		SessionID string
		Sequence  int64
	}
	if err := db.Table("events").Select("session_id, MAX(sequence) AS sequence").Where("session_id IN ?", ids).Group("session_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		through[row.SessionID] = row.Sequence
	}
	return through, nil
}
