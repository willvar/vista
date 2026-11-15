package store

import "context"

// MarkRead only acknowledges replies that already exist and were included in
// the caller's viewport. Concurrent or out-of-order reads never move backward.
func (s *Store) MarkRead(ctx context.Context, sessionID string, through int64) (int64, error) {
	var sequence int64
	err := s.orm.WithContext(ctx).Raw(`UPDATE sessions SET read_sequence=MAX(read_sequence,
  COALESCE((SELECT MAX(sequence) FROM messages WHERE session_id=? AND role='assistant' AND sequence<=?),0))
  WHERE id=? RETURNING read_sequence`, sessionID, through, sessionID).Row().Scan(&sequence)
	return sequence, err
}
