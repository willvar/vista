package store

import "context"

// SessionListSnapshot binds list metadata to each conversation's saved event
// boundary. The list does not advance a client's durable conversation cursor.
type SessionListSnapshot struct {
	Items            []Session        `json:"items"`
	ThroughSequences map[string]int64 `json:"throughSequences"`
}

func (s *Store) ListSnapshot(ctx context.Context, open ...bool) (SessionListSnapshot, error) {
	var snapshot SessionListSnapshot
	err := s.readView(ctx, func(view *Store) (err error) {
		if snapshot.Items, err = view.List(ctx, open...); err != nil {
			return err
		}
		snapshot.ThroughSequences = make(map[string]int64, len(snapshot.Items))
		ids := make([]string, 0, len(snapshot.Items))
		for _, item := range snapshot.Items {
			ids = append(ids, item.ID)
			snapshot.ThroughSequences[item.ID] = 0
		}
		if len(ids) == 0 {
			return nil
		}
		var boundaries []struct {
			SessionID string
			Sequence  int64
		}
		if err = view.orm.WithContext(ctx).Table("events").Select("session_id, MAX(sequence) AS sequence").Where("session_id IN ?", ids).Group("session_id").Scan(&boundaries).Error; err != nil {
			return err
		}
		for _, boundary := range boundaries {
			snapshot.ThroughSequences[boundary.SessionID] = boundary.Sequence
		}
		return nil
	})
	return snapshot, err
}
