package store

import "context"

// MessageResultBoundaries includes replies outside the current message page, so
// paging cannot charge an earlier reply's model calls to a later one.
func (s *Store) MessageResultBoundaries(ctx context.Context, sessionID string, messageIDs []string) (map[string]int64, error) {
	boundaries := make(map[string]int64, len(messageIDs))
	if len(messageIDs) == 0 {
		return boundaries, nil
	}
	var rows []struct {
		ID           string
		ResultsAfter int64
	}
	previous := s.orm.Table("messages AS previous").Select("MAX(previous.sequence)").
		Where("previous.session_id = reply.session_id AND previous.run_id = reply.run_id AND previous.role = ? AND previous.sequence < reply.sequence", "assistant")
	err := s.orm.WithContext(ctx).Table("messages AS reply").
		Select("reply.id, COALESCE((?), 0) AS results_after", previous).
		Where("reply.session_id = ? AND reply.id IN ? AND reply.role = ?", sessionID, messageIDs, "assistant").Scan(&rows).Error
	for _, row := range rows {
		boundaries[row.ID] = row.ResultsAfter
	}
	return boundaries, err
}
