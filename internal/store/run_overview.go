package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"unicode/utf8"

	"gorm.io/gorm"
)

type RunOverview struct {
	ModelCalls      int64   `json:"modelCalls"`
	FirstSequence   int64   `json:"firstSequence"`
	ThroughSequence int64   `json:"throughSequence"`
	StartedAt       string  `json:"startedAt"`
	FinishedAt      *string `json:"finishedAt,omitempty"`
	LastActivityAt  string  `json:"lastActivityAt,omitempty"`
	Events          []Event `json:"events"`
}

// Overview is a consistent snapshot, independent of event-page's 100-row page.
// It keeps only the first reasoning fragment per call and the latest tool
// previews. Full reasoning is loaded with the run's event pages on expansion.
func (s *Store) RunOverview(ctx context.Context, sessionID, runID string) (RunOverview, error) {
	return s.RunOverviewRange(ctx, sessionID, runID, 0, 0)
}

// RunOverviewRange restricts the snapshot to after < sequence <= through.
// A zero through leaves the tail open; a bounded range is an already completed
// reply segment and uses its last event time rather than the run's later end.
func (s *Store) RunOverviewRange(ctx context.Context, sessionID, runID string, after, through int64) (RunOverview, error) {
	overview := RunOverview{Events: []Event{}}
	if after < 0 || through < 0 || through > 0 && after >= through {
		return overview, ErrRunEventRange
	}
	err := s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run struct {
			CreatedAt  string
			FinishedAt *string
		}
		if err := tx.Table("runs").Select("created_at,finished_at").Where("id=? AND session_id=?", runID, sessionID).Take(&run).Error; err != nil {
			return err
		}
		overview.StartedAt, overview.FinishedAt = run.CreatedAt, run.FinishedAt
		base := tx.Table("events").Where("session_id=? AND json_extract(data,'$.runId')=? AND sequence>?", sessionID, runID, after)
		if through > 0 {
			base = base.Where("sequence<=?", through)
		}
		var totals struct {
			FirstSequence   int64
			ThroughSequence int64
			ModelCalls      int64
		}
		if err := base.Session(&gorm.Session{}).Select(`COALESCE(MIN(sequence),0) AS first_sequence,
   COALESCE(MAX(sequence),0) AS through_sequence,
   COALESCE(SUM(CASE WHEN kind='model_request' AND COALESCE(json_extract(data,'$.purpose'),'')!='summary' THEN 1 ELSE 0 END),0) AS model_calls`).Scan(&totals).Error; err != nil {
			return err
		}
		overview.FirstSequence, overview.ThroughSequence, overview.ModelCalls = totals.FirstSequence, totals.ThroughSequence, totals.ModelCalls
		if after > 0 || through > 0 {
			overview.StartedAt, overview.FinishedAt = "", nil
			if totals.FirstSequence > 0 {
				var first struct{ CreatedAt string }
				if err := tx.Table("events").Select("created_at").Where("session_id=? AND sequence=?", sessionID, totals.FirstSequence).Take(&first).Error; err != nil {
					return err
				}
				overview.StartedAt = first.CreatedAt
				if through == 0 {
					overview.FinishedAt = run.FinishedAt
				}
			}
		}
		if totals.ThroughSequence > 0 {
			var last struct{ CreatedAt string }
			if err := tx.Table("events").Select("created_at").Where("session_id=? AND sequence=?", sessionID, totals.ThroughSequence).Take(&last).Error; err != nil {
				return err
			}
			overview.LastActivityAt = last.CreatedAt
			if through > 0 {
				overview.FinishedAt = &last.CreatedAt
			}
		}
		var events []Event
		latestToolSequences := base.Session(&gorm.Session{}).Select("MAX(sequence)").
			Where("kind='tool' AND sequence<=?", totals.ThroughSequence).
			Group("COALESCE(json_extract(data,'$.tool.callId'),CAST(sequence AS TEXT))")
		firstReasoningSequences := base.Session(&gorm.Session{}).Select("MIN(sequence)").
			Where(`kind='model_reasoning_delta' AND sequence<=?
 AND COALESCE(json_extract(data,'$.purpose'),'')=''
 AND json_extract(data,'$.turn')>0 AND length(json_extract(data,'$.content'))>0`, totals.ThroughSequence).
			Group("COALESCE(NULLIF(json_extract(data,'$.taskId'),''),'main'),json_extract(data,'$.turn')")
		if err := base.Session(&gorm.Session{}).Select(`session_id,sequence,kind,created_at,
   CAST(CASE WHEN kind='run_finished'
    THEN json_object('runId',json_extract(data,'$.runId'),'status',json_extract(data,'$.status'))
    WHEN kind='tool' AND length(CAST(json_extract(data,'$.content') AS BLOB))>512
    THEN json_set(data,'$.content',substr(json_extract(data,'$.content'),1,512),'$.tool.truncated',json('true'))
    ELSE data END AS BLOB) AS data`).
			Where("sequence<=?", totals.ThroughSequence).
			Where("kind IN ? OR sequence IN (?) OR sequence IN (?)", []string{"agent_task", "checklist", "model_request", "run_finished", "source", "context_notice"}, latestToolSequences, firstReasoningSequences).
			Order("sequence").Find(&events).Error; err != nil {
			return err
		}
		latestTools := make(map[string]Event)
		var checklist *Event
		for _, event := range events {
			if event.Kind == "checklist" {
				current := event
				checklist = &current
				continue
			}
			if event.Kind != "tool" {
				overview.Events = append(overview.Events, event)
				continue
			}
			var data map[string]json.RawMessage
			if err := json.Unmarshal(event.Data, &data); err != nil {
				return err
			}
			var tool struct {
				CallID string `json:"callId"`
			}
			if err := json.Unmarshal(data["tool"], &tool); err != nil {
				return err
			}
			key := tool.CallID
			if key == "" {
				key = strconv.FormatInt(event.Sequence, 10)
			}
			event.Data = shortToolPreview(data)
			latestTools[key] = event
		}
		if checklist != nil {
			overview.Events = append(overview.Events, *checklist)
		}
		for _, event := range latestTools {
			overview.Events = append(overview.Events, event)
		}
		sort.Slice(overview.Events, func(i, j int) bool { return overview.Events[i].Sequence < overview.Events[j].Sequence })
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ErrNotFound
	}
	return overview, err
}

// CompactToolEvent returns the same tool preview used by RunOverview, without
// changing stored events or shortening model text and reasoning fragments.
func CompactToolEvent(event Event) Event {
	if event.Kind != "tool" {
		return event
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(event.Data, &data) != nil {
		return event
	}
	event.Data = shortToolPreview(data)
	return event
}

func shortToolPreview(data map[string]json.RawMessage) json.RawMessage {
	var content string
	_ = json.Unmarshal(data["content"], &content)
	if len(content) > 512 {
		end := 512
		for end > 0 && !utf8.RuneStart(content[end]) {
			end--
		}
		data["content"], _ = json.Marshal(content[:end])
		var tool map[string]json.RawMessage
		if json.Unmarshal(data["tool"], &tool) == nil {
			tool["truncated"] = json.RawMessage("true")
			data["tool"], _ = json.Marshal(tool)
		}
	}
	encoded, _ := json.Marshal(data)
	return encoded
}
