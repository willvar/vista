package store

import (
	"context"
	"database/sql"
)

// MainOutput is a display index, not a conversation message. Sequence is the
// first text event, so later interjections cannot move an output.
type MainOutput struct {
	RunID        string `json:"runId"`
	TaskID       string `json:"taskId"`
	Turn         int32  `json:"turn"`
	Sequence     int64  `json:"sequence"`
	LastSequence int64  `json:"lastSequence"`
	CreatedAt    string `json:"createdAt"`
	Provider     string `json:"provider,omitempty"`
	ModelID      string `json:"modelId,omitempty"`
	ModelKey     string `json:"modelKey,omitempty"`
	Bytes        int64  `json:"bytes"`
	Complete     bool   `json:"complete"`
}

// MainOutputs returns the latest 100 display records in chronological order.
// Before is an exclusive first-event sequence, matching existing event pages.
// Full bodies remain in model-output. Reasoning belongs to the execution trace.
func (s *Store) MainOutputs(ctx context.Context, sessionID, runID string, before int64) ([]MainOutput, error) {
	if before < 0 {
		return nil, ErrRunEventRange
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, "SELECT status FROM runs WHERE session_id=? AND id=?", sessionID, runID).Scan(&status); err != nil {
		if err == sql.ErrNoRows {
			err = ErrNotFound
		}
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `WITH outputs AS (
 SELECT turn, MIN(event_sequence) AS sequence, MAX(event_sequence) AS last_sequence, MAX(offset+length) AS bytes
 FROM model_output_chunks WHERE session_id=? AND run_id=? GROUP BY turn
), calls AS (
 SELECT json_extract(data,'$.turn') AS turn,
 MAX(CASE WHEN kind='model_request' THEN json_extract(data,'$.provider') ELSE json_extract(data,'$.modelResult.provider') END) AS provider,
 MAX(CASE WHEN kind='model_request' THEN json_extract(data,'$.id') ELSE json_extract(data,'$.modelResult.id') END) AS model_id,
 MAX(CASE WHEN kind='model_request' THEN json_extract(data,'$.key') END) AS model_key,
 MAX(kind='model_result') AS complete
 FROM events WHERE session_id=? AND json_extract(data,'$.runId')=?
 AND kind IN ('model_request','model_result')
 AND COALESCE(json_extract(data,'$.depth'),0)=0
 AND COALESCE(NULLIF(json_extract(data,'$.taskId'),''),'main')='main'
 GROUP BY json_extract(data,'$.turn')
)
 SELECT o.turn,o.sequence,o.last_sequence,e.created_at,
 COALESCE(c.provider,''),COALESCE(c.model_id,''),COALESCE(c.model_key,''),o.bytes,
 COALESCE(c.complete,0) OR ?!='running'
 FROM outputs o JOIN events e ON e.session_id=? AND e.sequence=o.sequence
 LEFT JOIN calls c ON c.turn=o.turn
 WHERE COALESCE(json_extract(e.data,'$.depth'),0)=0
 AND COALESCE(NULLIF(json_extract(e.data,'$.taskId'),''),'main')='main'
 AND (?=0 OR o.sequence<?)
 ORDER BY o.sequence DESC LIMIT 100`, sessionID, runID, sessionID, runID, status, sessionID, before, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	outputs := []MainOutput{}
	for rows.Next() {
		output := MainOutput{RunID: runID, TaskID: "main"}
		if err := rows.Scan(&output.Turn, &output.Sequence, &output.LastSequence, &output.CreatedAt, &output.Provider, &output.ModelID, &output.ModelKey, &output.Bytes, &output.Complete); err != nil {
			return nil, err
		}
		outputs = append(outputs, output)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for left, right := 0, len(outputs)-1; left < right; left, right = left+1, right-1 {
		outputs[left], outputs[right] = outputs[right], outputs[left]
	}
	return outputs, nil
}
