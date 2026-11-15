package store

import (
	"context"
	"encoding/json"
	"errors"

	"vista/internal/workerproto"
)

func (s *Store) ContextUsage(ctx context.Context, sessionID string) (*workerproto.ContextUsage, error) {
	var raw []byte
	var sequence int64
	err := s.orm.WithContext(ctx).Raw(`SELECT sequence, CASE WHEN kind='context_usage' THEN data ELSE json_extract(data,'$.contextUsage') END FROM events WHERE session_id=? AND (kind='context_usage' OR (kind='context_notice' AND json_extract(data,'$.depth')=0 AND json_type(data,'$.contextUsage')='object')) ORDER BY sequence DESC LIMIT 1`, sessionID).Row().Scan(&sequence, &raw)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var usage workerproto.ContextUsage
	if err := json.Unmarshal(raw, &usage); err != nil {
		return nil, err
	}
	usage.Sequence = sequence
	return &usage, nil
}
