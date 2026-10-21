package store

import (
	"context"
	"encoding/json"
)

// ToolArtifact resolves the latest artifact for a call in one run. Task prefixes
// in call IDs distinguish sibling subagents without a second storage index.
func (s *Store) ToolArtifact(ctx context.Context, sessionID, runID, callID string) (Artifact, error) {
	var event Event
	err := s.orm.WithContext(ctx).Table("events").Select("CAST(data AS BLOB) AS data").
		Where("session_id=? AND kind='artifact' AND json_extract(data,'$.runId')=? AND json_extract(data,'$.callId')=?", sessionID, runID, callID).
		Order("sequence DESC").Take(&event).Error
	if err != nil {
		return Artifact{}, err
	}
	var link struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(event.Data, &link); err != nil {
		return Artifact{}, err
	}
	return s.GetArtifact(ctx, sessionID, link.ID)
}
