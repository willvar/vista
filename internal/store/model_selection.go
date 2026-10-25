package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Preferences belongs to a persistent local identity, not a browser or session.
type Preferences struct {
	UserID                 string            `json:"userId"`
	LastMode               string            `json:"lastMode"`
	LastTier               string            `json:"lastTier"`
	ManualModelKey         string            `json:"manualModelKey"`
	ReasoningByModelID     map[string]string `json:"reasoningByModelId"`
	FeedbackInvitationSeen bool              `json:"feedbackInvitationSeen"`
}

type PreferencesPatch struct {
	LastMode               *string           `json:"lastMode"`
	LastTier               *string           `json:"lastTier"`
	ManualModelKey         *string           `json:"manualModelKey"`
	ReasoningByModelID     map[string]string `json:"reasoningByModelId"`
	FeedbackInvitationSeen *bool             `json:"feedbackInvitationSeen"`
}

type preferenceRow struct {
	ID                     string `gorm:"primaryKey"`
	UserID                 string `gorm:"uniqueIndex;not null"`
	LastMode               string
	LastTier               string
	ManualModelKey         string
	ReasoningJSON          string
	FeedbackInvitationSeen bool
}

func (preferenceRow) TableName() string { return "model_selection_preferences" }

type runSelectionRow struct {
	RunID           string `gorm:"primaryKey"`
	ModelID         string
	ReasoningEffort string
	Endpoint        string
}

type observationReceipt struct {
	RequestID  string `gorm:"primaryKey"`
	ObservedAt time.Time
}

type modelConnectionHealth struct {
	ModelID    string    `gorm:"primaryKey"`
	Endpoint   string    `gorm:"primaryKey"`
	RetryAfter time.Time `gorm:"index"`
}

func (modelConnectionHealth) TableName() string { return "model_connection_health" }

func (observationReceipt) TableName() string { return "model_observation_receipts" }

type selectionQueuedRow struct {
	ID            string `gorm:"primaryKey"`
	Debug         bool   `gorm:"column:debug;not null;default:false"`
	SelectionMode string `gorm:"column:selection_mode;not null;default:''"`
	SelectionTier string `gorm:"column:selection_tier;not null;default:''"`
}

func (selectionQueuedRow) TableName() string { return "queued_inputs" }

func (runSelectionRow) TableName() string { return "run_model_selections" }

type feedbackRow struct {
	UserID          string `gorm:"primaryKey;index:feedback_by_model,priority:1"`
	MessageID       string `gorm:"primaryKey"`
	SessionID       string `gorm:"index"`
	ModelID         string `gorm:"index:feedback_by_model,priority:2"`
	ReasoningEffort string `gorm:"index:feedback_by_model,priority:3"`
	Value           string
	ReplyAt         string `gorm:"index:feedback_by_model,priority:4"`
}

func (feedbackRow) TableName() string { return "model_reply_feedback" }

// ModelObservation stores identities and measurements only, never message text.
type ModelObservation struct {
	RequestID       string    `gorm:"primaryKey" json:"requestId"`
	UserID          string    `gorm:"-" json:"-"`
	ModelID         string    `gorm:"index:observations_by_model,priority:1" json:"modelId"`
	ReasoningEffort string    `gorm:"index:observations_by_model,priority:2" json:"reasoningEffort"`
	Endpoint        string    `json:"endpoint"`
	InputTokens     int64     `json:"inputTokens"`
	InputBucket     int       `gorm:"index:observations_by_model,priority:3" json:"inputBucket"`
	FirstTextMS     *float64  `json:"firstTextMs"`
	CacheReadTokens int64     `json:"cacheReadTokens"`
	Outcome         string    `json:"outcome"`
	Purpose         string    `json:"purpose"`
	ObservedAt      time.Time `gorm:"index:observations_by_model,priority:4" json:"observedAt"`
}

func (ModelObservation) TableName() string { return "model_speed_observations" }

type attemptRow struct {
	Scope           string `gorm:"primaryKey"`
	ModelID         string `gorm:"primaryKey"`
	ReasoningEffort string `gorm:"primaryKey"`
	Attempts        int
	Explorations    int
	LastAttempt     time.Time
	PendingID       string
	PendingAt       time.Time
	Outcome         string
}

func (attemptRow) TableName() string { return "model_selection_attempts" }

// TierSelection remembers this session's chosen model independently for each
// automatic tier. Manual selection and the active run cannot overwrite it.
type TierSelection struct {
	ModelKey        string
	ReasoningEffort string
}

type sessionTierSelectionRow struct {
	SessionID       string `gorm:"primaryKey;not null"`
	Tier            string `gorm:"primaryKey;not null"`
	ModelKey        string `gorm:"not null"`
	ReasoningEffort string
}

func (sessionTierSelectionRow) TableName() string { return "session_tier_selections" }

func validSessionTier(tier string) bool { return tier == "speed" || tier == "quality" }

func rememberCurrentTier(tx *gorm.DB, current sessionRow) error {
	if current.SelectionMode != "auto" || !validSessionTier(current.SelectionTier) || current.ModelKey == "" || current.SelectionReasoningEffort == "" {
		return nil
	}
	// An existing affinity is authoritative. A later manual run can change the
	// active model field without changing the remembered automatic choice.
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&sessionTierSelectionRow{
		SessionID: current.ID, Tier: current.SelectionTier, ModelKey: current.ModelKey, ReasoningEffort: current.SelectionReasoningEffort,
	}).Error
}

func bindTier(tx *gorm.DB, id, tier, key, effort string) error {
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "session_id"}, {Name: "tier"}},
		DoUpdates: clause.AssignmentColumns([]string{"model_key", "reasoning_effort"}),
	}).Create(&sessionTierSelectionRow{SessionID: id, Tier: tier, ModelKey: key, ReasoningEffort: effort}).Error
}

func (s *Store) SessionTierSelection(ctx context.Context, id, tier string) (*TierSelection, error) {
	if !validSessionTier(tier) {
		return nil, errors.New("invalid selection tier")
	}
	var selected *TierSelection
	err := s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current sessionRow
		if err := tx.Where("id=?", id).Take(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		var affinity sessionTierSelectionRow
		result := tx.Where("session_id=? AND tier=?", id, tier).Find(&affinity)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			selected = &TierSelection{ModelKey: affinity.ModelKey, ReasoningEffort: affinity.ReasoningEffort}
			return nil
		}
		// Current fields are usable only while they unambiguously belong to this
		// automatic tier, before the user has switched away from that binding.
		if current.SelectionMode == "auto" && current.SelectionTier == tier && current.ModelKey != "" && current.SelectionReasoningEffort != "" {
			selected = &TierSelection{ModelKey: current.ModelKey, ReasoningEffort: current.SelectionReasoningEffort}
		}
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	return selected, err
}

func (s *Store) BindSessionTierSelection(ctx context.Context, id, tier, key, effort string) error {
	if !validSessionTier(tier) || key == "" {
		return errors.New("invalid tier selection")
	}
	return s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current sessionRow
		if err := tx.Where("id=?", id).Take(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if current.SelectionMode != "auto" || current.SelectionTier != tier {
			if err := rememberCurrentTier(tx, current); err != nil {
				return err
			}
		}
		if err := bindTier(tx, id, tier, key, effort); err != nil {
			return err
		}
		return sessionRowsAffected(tx.Model(&sessionRow{}).Where("id=?", id).Updates(map[string]any{
			"selection_mode": "auto", "selection_tier": tier, "model_key": key, "selection_reasoning_effort": effort,
		}))
	})
}

type SessionSelectionPatch struct {
	SelectionMode         *string `json:"selectionMode"`
	SelectionTier         *string `json:"selectionTier"`
	ManualModelKey        *string `json:"manualModelKey"`
	ManualReasoningEffort *string `json:"manualReasoningEffort"`
}

func (s *Store) initModelSelectionSchema() error {
	// Add session settings and read position through GORM.
	for _, field := range []string{"SelectionMode", "SelectionTier", "ManualModelKey", "ManualReasoningEffort", "SelectionReasoningEffort", "InputQueuePaused", "ReadSequence"} {
		if !s.orm.Migrator().HasColumn(&sessionRow{}, field) {
			if err := s.orm.Migrator().AddColumn(&sessionRow{}, field); err != nil {
				return err
			}
		}
	}
	for _, field := range []string{"SelectionMode", "SelectionTier", "Debug"} {
		if !s.orm.Migrator().HasColumn(&selectionQueuedRow{}, field) {
			if err := s.orm.Migrator().AddColumn(&selectionQueuedRow{}, field); err != nil {
				return err
			}
		}
	}
	if err := s.orm.AutoMigrate(&preferenceRow{}, &runSelectionRow{}, &feedbackRow{}, &ModelObservation{}, &observationReceipt{}, &attemptRow{}, &modelConnectionHealth{}, &sessionTierSelectionRow{}); err != nil {
		return err
	}
	// A server restart ends previous executions. Interrupted first attempts count
	// as attempts, so a failing connection cannot stay forever "untried".
	return s.orm.Model(&attemptRow{}).Where("pending_id <> ?", "").Updates(map[string]any{
		"attempts": gorm.Expr("attempts + 1"), "pending_id": "", "outcome": "interrupted",
	}).Error
}

func preferences(tx *gorm.DB) (Preferences, error) {
	row := preferenceRow{ID: "local", UserID: uuid.NewString(), LastMode: "manual", LastTier: "speed", ReasoningJSON: "{}"}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return Preferences{}, err
	}
	if err := tx.Where("id = ?", "local").Take(&row).Error; err != nil {
		return Preferences{}, err
	}
	result := Preferences{UserID: row.UserID, LastMode: row.LastMode, LastTier: row.LastTier, ManualModelKey: row.ManualModelKey, FeedbackInvitationSeen: row.FeedbackInvitationSeen, ReasoningByModelID: map[string]string{}}
	if err := json.Unmarshal([]byte(row.ReasoningJSON), &result.ReasoningByModelID); err != nil {
		return Preferences{}, err
	}
	if result.ReasoningByModelID == nil {
		result.ReasoningByModelID = map[string]string{}
	}
	return result, nil
}

func (s *Store) Preferences(ctx context.Context) (Preferences, error) {
	var result Preferences
	err := s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = preferences(tx)
		return err
	})
	return result, err
}

func (s *Store) PatchPreferences(ctx context.Context, patch PreferencesPatch) (Preferences, error) {
	var result Preferences
	err := s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = preferences(tx)
		if err != nil {
			return err
		}
		if patch.LastMode != nil {
			result.LastMode = *patch.LastMode
		}
		if patch.LastTier != nil {
			result.LastTier = *patch.LastTier
		}
		if patch.ManualModelKey != nil {
			result.ManualModelKey = *patch.ManualModelKey
		}
		if patch.FeedbackInvitationSeen != nil {
			result.FeedbackInvitationSeen = result.FeedbackInvitationSeen || *patch.FeedbackInvitationSeen
		}
		for id, value := range patch.ReasoningByModelID {
			result.ReasoningByModelID[id] = value
		}
		encoded, err := json.Marshal(result.ReasoningByModelID)
		if err != nil {
			return err
		}
		return tx.Model(&preferenceRow{}).Where("id = ?", "local").Updates(map[string]any{
			"last_mode": result.LastMode, "last_tier": result.LastTier, "manual_model_key": result.ManualModelKey,
			"reasoning_json": string(encoded), "feedback_invitation_seen": result.FeedbackInvitationSeen,
		}).Error
	})
	return result, err
}

func (s *Store) PatchSessionSelection(ctx context.Context, id string, patch SessionSelectionPatch) (Session, error) {
	err := s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current sessionRow
		if err := tx.Where("id=?", id).Take(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return sql.ErrNoRows
			}
			return err
		}
		values := map[string]any{}
		if patch.SelectionMode != nil && *patch.SelectionMode != current.SelectionMode || patch.SelectionTier != nil && *patch.SelectionTier != current.SelectionTier {
			if err := rememberCurrentTier(tx, current); err != nil {
				return err
			}
			values["selection_reasoning_effort"] = ""
		}
		if patch.SelectionMode != nil {
			values["selection_mode"] = *patch.SelectionMode
		}
		if patch.SelectionTier != nil {
			values["selection_tier"] = *patch.SelectionTier
		}
		if patch.ManualModelKey != nil {
			values["manual_model_key"] = *patch.ManualModelKey
		}
		if patch.ManualReasoningEffort != nil {
			values["manual_reasoning_effort"] = *patch.ManualReasoningEffort
		}
		if len(values) == 0 {
			return nil
		}
		return sessionRowsAffected(tx.Model(&sessionRow{}).Where("id=?", id).Updates(values))
	})
	if err != nil {
		return Session{}, err
	}
	return s.Get(ctx, id)
}

// SelectionInputEstimate includes the recent conversation and stored summary,
// rather than treating a follow-up as an isolated short prompt.
func (s *Store) SelectionInputEstimate(ctx context.Context, sessionID string, promptTokens int64, hasImages bool) (int64, error) {
	checkpoint, err := s.ContextCheckpoint(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	var checkpointTokens int64
	if checkpoint != nil {
		checkpointTokens = checkpoint.InputTokens
	}
	entries, err := s.ContextEntries(ctx, sessionID, checkpoint, 0)
	if err != nil {
		return 0, err
	}
	// Approximate ASCII at three characters/token and non-ASCII at two
	// tokens/rune. Provider usage corrects observation buckets afterward.
	total := promptTokens + checkpointTokens + 2048
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.History != nil {
			total += entry.History.History.Tokens
			continue
		}
		row := entry.Message
		total += SelectionTextTokens(row.Content) + 12
		if row.OutputBytes > len(row.Content) {
			total += int64(row.OutputBytes-len(row.Content)+2) / 3
		}
		ids = append(ids, row.ID)
	}
	if len(ids) > 0 {
		var imageCount int64
		if err := s.orm.WithContext(ctx).Table("message_resources").Where("message_id IN ?", ids).Count(&imageCount).Error; err != nil {
			return 0, err
		}
		total += imageCount * 4096
	}
	if hasImages {
		total += 4096
	}
	return total, nil
}

func SelectionTextTokens(text string) int64 {
	var ascii, other int64
	for _, r := range text {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			other++
		}
	}
	return (ascii+2)/3 + other*2
}

// BindSessionSelection remembers an automatic selection without overwriting
// the user's manual model or manual reasoning configuration.
func (s *Store) BindSessionSelection(ctx context.Context, id, modelKey, effort string) error {
	return s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current sessionRow
		if err := tx.Where("id=?", id).Take(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return sql.ErrNoRows
			}
			return err
		}
		if current.SelectionMode == "auto" && validSessionTier(current.SelectionTier) {
			if err := bindTier(tx, id, current.SelectionTier, modelKey, effort); err != nil {
				return err
			}
		}
		return sessionRowsAffected(tx.Model(&sessionRow{}).Where("id=?", id).Updates(map[string]any{
			"model_key": modelKey, "selection_reasoning_effort": effort,
		}))
	})
}

// RunReasoningEfforts reads the actual saved selection for historical results
// recorded before model-result events included their display metadata.
func (s *Store) RunReasoningEfforts(ctx context.Context, runIDs []string) (map[string]string, error) {
	result := make(map[string]string)
	if len(runIDs) == 0 {
		return result, nil
	}
	var rows []runSelectionRow
	if err := s.orm.WithContext(ctx).Where("run_id IN ?", runIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.RunID] = row.ReasoningEffort
	}
	return result, nil
}

func (s *Store) RecordRunSelection(ctx context.Context, runID, modelID, effort, endpoint string) error {
	if runID == "" || modelID == "" {
		return errors.New("missing run model identity")
	}
	return s.orm.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&runSelectionRow{RunID: runID, ModelID: modelID, ReasoningEffort: effort, Endpoint: endpoint}).Error
}

type ReplyFeedback struct {
	MessageID       string  `json:"messageId"`
	Value           *string `json:"value"`
	ModelID         string  `json:"modelId"`
	ReasoningEffort string  `json:"reasoningEffort"`
}

var ErrFeedbackTarget = errors.New("feedback requires an assistant reply with a model identity")

func (s *Store) SetReplyFeedback(ctx context.Context, userID, sessionID, messageID string, value *string) (ReplyFeedback, error) {
	if value != nil && *value != "up" && *value != "down" {
		return ReplyFeedback{}, errors.New("invalid feedback value")
	}
	result := ReplyFeedback{MessageID: messageID, Value: value}
	err := s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var message struct{ ID, RunID, ModelID, Role, CreatedAt string }
		if err := tx.Table("messages").Select("id, run_id, model_id, role, created_at").Where("id = ? AND session_id = ?", messageID, sessionID).Take(&message).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return sql.ErrNoRows
			}
			return err
		}
		if message.Role != "assistant" || message.ModelID == "" {
			return ErrFeedbackTarget
		}
		result.ModelID = message.ModelID
		var run runSelectionRow
		if err := tx.Where("run_id = ?", message.RunID).Take(&run).Error; err == nil {
			// The immutable reply identity wins over a selector or later run.
			if run.ModelID == message.ModelID {
				result.ReasoningEffort = run.ReasoningEffort
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if result.ReasoningEffort == "" {
			result.ReasoningEffort = "default"
		}
		if value == nil {
			return tx.Where("user_id = ? AND message_id = ?", userID, messageID).Delete(&feedbackRow{}).Error
		}
		row := feedbackRow{UserID: userID, MessageID: messageID, SessionID: sessionID, ModelID: message.ModelID, ReasoningEffort: result.ReasoningEffort, Value: *value, ReplyAt: message.CreatedAt}
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error
	})
	return result, err
}

func (s *Store) SessionFeedback(ctx context.Context, userID, sessionID string) (map[string]string, error) {
	var rows []feedbackRow
	err := s.orm.WithContext(ctx).Where("user_id = ? AND session_id = ?", userID, sessionID).Find(&rows).Error
	result := make(map[string]string, len(rows))
	for _, row := range rows {
		result[row.MessageID] = row.Value
	}
	return result, err
}

type QualityStats struct {
	Up, Down, Count int
	Score           float64
	Known           bool
}

func WilsonScore(up, down int) float64 {
	n := float64(up + down)
	if n == 0 {
		return 0
	}
	const z = 1.2815515655
	p, z2 := float64(up)/n, z*z
	return math.Max(0, (p+z2/(2*n)-z*math.Sqrt(p*(1-p)/n+z2/(4*n*n)))/(1+z2/n))
}

func qualityStats(tx *gorm.DB, userID, modelID, effort string) (QualityStats, error) {
	var rows []feedbackRow
	if err := tx.Where("user_id = ? AND model_id = ? AND reasoning_effort = ?", userID, modelID, effort).Order("reply_at DESC, message_id DESC").Limit(200).Find(&rows).Error; err != nil {
		return QualityStats{}, err
	}
	result := QualityStats{Count: len(rows), Known: len(rows) > 0}
	for _, row := range rows {
		if row.Value == "up" {
			result.Up++
		} else {
			result.Down++
		}
	}
	result.Score = WilsonScore(result.Up, result.Down)
	return result, nil
}

func (s *Store) ModelQuality(ctx context.Context, userID, modelID, effort string) (QualityStats, error) {
	return qualityStats(s.orm.WithContext(ctx), userID, modelID, effort)
}

func ModelInputBucket(tokens int64) int {
	switch {
	case tokens <= 4096:
		return 0
	case tokens <= 16384:
		return 1
	case tokens <= 65536:
		return 2
	default:
		return 3
	}
}

func (s *Store) RecordModelObservation(ctx context.Context, observation ModelObservation) error {
	if observation.RequestID == "" || observation.ModelID == "" || observation.InputTokens < 0 || observation.CacheReadTokens < 0 {
		return errors.New("invalid model observation")
	}
	if observation.FirstTextMS != nil && (math.IsNaN(*observation.FirstTextMS) || math.IsInf(*observation.FirstTextMS, 0) || *observation.FirstTextMS < 0) {
		return errors.New("invalid first text duration")
	}
	if observation.ObservedAt.IsZero() {
		observation.ObservedAt = time.Now().UTC()
	}
	if observation.ReasoningEffort == "" {
		observation.ReasoningEffort = "default"
	}
	if observation.Purpose == "" {
		observation.Purpose = "conversation"
	}
	observation.InputBucket = ModelInputBucket(observation.InputTokens)
	return s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		receipt := observationReceipt{RequestID: observation.RequestID, ObservedAt: observation.ObservedAt}
		seen := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&receipt)
		if seen.Error != nil {
			return seen.Error
		}
		if seen.RowsAffected == 0 {
			return nil
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&observation)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		if observation.Purpose != "conversation" {
			return nil
		}
		if observation.Endpoint != "" {
			if observation.Outcome == "failed" {
				if err := noteConnectionFailure(tx, observation.ModelID, observation.Endpoint); err != nil {
					return err
				}
			} else if observation.Outcome == "completed" || observation.Outcome == "succeeded" || observation.Outcome == "no_text" {
				if err := tx.Where("model_id = ? AND endpoint = ?", observation.ModelID, observation.Endpoint).Delete(&modelConnectionHealth{}).Error; err != nil {
					return err
				}
			}
		}
		if err := finishAttempts(tx, observation.UserID, observation.ModelID, observation.ReasoningEffort, observation.Outcome, observation.ObservedAt); err != nil {
			return err
		}
		// Cap valid first-text samples per exact model/configuration/input bucket.
		// Keep failure/no-body outcomes bounded separately; neither is a zero sample.
		for _, valid := range []bool{true, false} {
			query := tx.Model(&ModelObservation{}).Where("model_id = ? AND reasoning_effort = ? AND input_bucket = ? AND purpose = ?", observation.ModelID, observation.ReasoningEffort, observation.InputBucket, "conversation")
			if valid {
				query = query.Where("first_text_ms IS NOT NULL")
			} else {
				query = query.Where("first_text_ms IS NULL")
			}
			var retained []string
			if err := query.Order("observed_at DESC, request_id DESC").Limit(200).Pluck("request_id", &retained).Error; err != nil {
				return err
			}
			cleanup := tx.Where("model_id = ? AND reasoning_effort = ? AND input_bucket = ? AND purpose = ?", observation.ModelID, observation.ReasoningEffort, observation.InputBucket, "conversation")
			if valid {
				cleanup = cleanup.Where("first_text_ms IS NOT NULL")
			} else {
				cleanup = cleanup.Where("first_text_ms IS NULL")
			}
			cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
			if len(retained) == 200 {
				cleanup = cleanup.Where("observed_at < ? OR request_id NOT IN ?", cutoff, retained)
			} else {
				cleanup = cleanup.Where("observed_at < ?", cutoff)
			}
			if err := cleanup.Delete(&ModelObservation{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func finishAttempts(tx *gorm.DB, userID, modelID, effort, outcome string, at time.Time) error {
	scopes := []string{"speed"}
	if userID != "" {
		scopes = append(scopes, "quality:"+userID)
	}
	for _, scope := range scopes {
		row := attemptRow{Scope: scope, ModelID: modelID, ReasoningEffort: effort, Attempts: 1, LastAttempt: at, Outcome: outcome}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "scope"}, {Name: "model_id"}, {Name: "reasoning_effort"}}, DoUpdates: clause.Assignments(map[string]any{
			"attempts": gorm.Expr("attempts + 1"), "last_attempt": at, "pending_id": "", "outcome": outcome,
		})}).Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

// FinishModelAttempt covers cancellation/admission/dispatch failure before any
// provider result exists. It prevents a reserved ID being stuck as pending.
func (s *Store) FinishModelAttempt(ctx context.Context, userID, modelID, effort, outcome string) error {
	return s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return finishAttempts(tx, userID, modelID, effort, outcome, time.Now().UTC()) })
}

func (s *Store) CancelPendingSelection(ctx context.Context, userID, modelID, effort, outcome string) error {
	scopes := []string{"speed", "quality:" + userID}
	return s.orm.WithContext(ctx).Model(&attemptRow{}).Where("scope IN ? AND model_id = ? AND reasoning_effort = ? AND pending_id <> ?", scopes, modelID, effort, "").Updates(map[string]any{"attempts": gorm.Expr("attempts + 1"), "pending_id": "", "last_attempt": time.Now().UTC(), "outcome": outcome}).Error
}

func noteConnectionFailure(tx *gorm.DB, modelID, endpoint string) error {
	return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&modelConnectionHealth{ModelID: modelID, Endpoint: endpoint, RetryAfter: time.Now().UTC().Add(time.Minute)}).Error
}
func (s *Store) NoteConnectionFailure(ctx context.Context, modelID, endpoint string) error {
	return noteConnectionFailure(s.orm.WithContext(ctx), modelID, endpoint)
}

func connectionCooldowns(tx *gorm.DB) (map[string]bool, error) {
	var rows []modelConnectionHealth
	err := tx.Where("retry_after > ?", time.Now().UTC()).Find(&rows).Error
	result := map[string]bool{}
	for _, row := range rows {
		result[row.ModelID+"\x00"+row.Endpoint] = true
	}
	return result, err
}
func (s *Store) ConnectionCooldowns(ctx context.Context) (map[string]bool, error) {
	return connectionCooldowns(s.orm.WithContext(ctx))
}

type SpeedStats struct {
	Count                          int
	MedianMS, IQRMS                float64
	EndpointMatched, LowConfidence bool
	Known                          bool
}

func percentile(sorted []float64, p float64) float64 {
	position := p * float64(len(sorted)-1)
	lo := int(position)
	hi := min(lo+1, len(sorted)-1)
	return sorted[lo] + (sorted[hi]-sorted[lo])*(position-float64(lo))
}

func speedStats(tx *gorm.DB, modelID, effort, endpoint string, bucket int, now time.Time) (SpeedStats, error) {
	var rows []ModelObservation
	if err := tx.Where("model_id = ? AND reasoning_effort = ? AND input_bucket = ? AND purpose = ? AND first_text_ms IS NOT NULL AND observed_at >= ?", modelID, effort, bucket, "conversation", now.Add(-30*24*time.Hour)).Order("observed_at DESC, request_id DESC").Limit(200).Find(&rows).Error; err != nil {
		return SpeedStats{}, err
	}
	values, matched := []float64{}, []float64{}
	for _, row := range rows {
		values = append(values, *row.FirstTextMS)
		if row.Endpoint == endpoint && endpoint != "" {
			matched = append(matched, *row.FirstTextMS)
		}
	}
	result := SpeedStats{}
	if len(matched) >= 5 {
		values, result.EndpointMatched = matched, true
	}
	result.Count, result.Known, result.LowConfidence = len(values), len(values) > 0, len(values) < 5
	if len(values) > 0 {
		sort.Float64s(values)
		result.MedianMS, result.IQRMS = percentile(values, .5), percentile(values, .75)-percentile(values, .25)
	}
	return result, nil
}

func (s *Store) ModelSpeed(ctx context.Context, modelID, effort, endpoint string, inputTokens int64) (SpeedStats, error) {
	return speedStats(s.orm.WithContext(ctx), modelID, effort, endpoint, ModelInputBucket(inputTokens), time.Now().UTC())
}

type SelectionCandidate struct{ ModelID, ModelKey, ReasoningEffort, Endpoint string }
type SelectionDecision struct {
	SelectionCandidate
	Reason        string
	LowConfidence bool
	SampleCount   int
}

type rankedCandidate struct {
	SelectionCandidate
	attempt attemptRow
	speed   SpeedStats
	quality QualityStats
}

// SelectModel atomically reserves cold-start candidates. A duplicate connection
// never gives a model ID an extra ticket in either ranking or exploration.
func (s *Store) SelectModel(ctx context.Context, userID, tier string, candidates []SelectionCandidate, inputTokens int64) (SelectionDecision, error) {
	return s.selectModel(ctx, userID, tier, candidates, inputTokens, rand.IntN)
}

func (s *Store) selectModel(ctx context.Context, userID, tier string, candidates []SelectionCandidate, inputTokens int64, draw func(int) int) (SelectionDecision, error) {
	if len(candidates) == 0 {
		return SelectionDecision{}, errors.New("no eligible model is configured")
	}
	if tier != "speed" && tier != "quality" {
		return SelectionDecision{}, errors.New("invalid selection tier")
	}
	var result SelectionDecision
	err := s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		cooldowns, err := connectionCooldowns(tx)
		if err != nil {
			return err
		}
		scope := "speed"
		if tier == "quality" {
			scope = "quality:" + userID
		}
		all, unknown, available := []rankedCandidate{}, []rankedCandidate{}, []rankedCandidate{}
		seen := map[string]bool{}
		for _, candidate := range candidates {
			if cooldowns[candidate.ModelID+"\x00"+candidate.Endpoint] {
				continue
			}
			if seen[candidate.ModelID] {
				continue
			}
			seen[candidate.ModelID] = true
			if candidate.ReasoningEffort == "" {
				candidate.ReasoningEffort = "default"
			}
			item := rankedCandidate{SelectionCandidate: candidate, attempt: attemptRow{Scope: scope, ModelID: candidate.ModelID, ReasoningEffort: candidate.ReasoningEffort}}
			if err := tx.Where("scope = ? AND model_id = ? AND reasoning_effort = ?", scope, candidate.ModelID, candidate.ReasoningEffort).Limit(1).Find(&item.attempt).Error; err != nil {
				return err
			}
			pending := item.attempt.PendingID != "" && now.Sub(item.attempt.PendingAt) < 30*time.Minute
			if item.attempt.PendingID != "" && !pending {
				item.attempt.Attempts++
				item.attempt.PendingID = ""
				item.attempt.Outcome = "interrupted"
				if err := tx.Save(&item.attempt).Error; err != nil {
					return err
				}
			}
			all = append(all, item)
			if !pending {
				available = append(available, item)
				if item.attempt.Attempts == 0 {
					unknown = append(unknown, item)
				}
			}
		}
		if len(available) == 0 {
			available = all
		}
		if len(available) == 0 {
			return errors.New("all eligible model connections are temporarily unavailable")
		}
		var chosen rankedCandidate
		reason := "recommended"
		if len(unknown) > 0 {
			chosen = unknown[draw(len(unknown))]
			reason = "first_use"
		} else {
			pool := []rankedCandidate{}
			for i := range available {
				item := &available[i]
				var err error
				if tier == "speed" {
					item.speed, err = speedStats(tx, item.ModelID, item.ReasoningEffort, item.Endpoint, ModelInputBucket(inputTokens), now)
					if item.speed.Known {
						pool = append(pool, *item)
					}
				} else {
					item.quality, err = qualityStats(tx, userID, item.ModelID, item.ReasoningEffort)
					if item.quality.Known {
						pool = append(pool, *item)
					}
				}
				if err != nil {
					return err
				}
			}
			if len(pool) == 0 {
				chosen = rotateCandidate(available, draw)
				reason = "no_data"
			} else {
				if tier == "speed" {
					sort.SliceStable(pool, func(i, j int) bool {
						if pool[i].speed.MedianMS != pool[j].speed.MedianMS {
							return pool[i].speed.MedianMS < pool[j].speed.MedianMS
						}
						return pool[i].speed.IQRMS < pool[j].speed.IQRMS
					})
					best := pool[0].speed.MedianMS
					end := 0
					for end < len(pool) && end < 10 && pool[end].speed.MedianMS <= best*1.25 {
						end++
					}
					pool = pool[:end]
				} else {
					sort.SliceStable(pool, func(i, j int) bool { return pool[i].quality.Score > pool[j].quality.Score })
					best := pool[0].quality.Score
					end := 0
					for end < len(pool) && end < 10 && pool[end].quality.Score >= best-.10 {
						end++
					}
					pool = pool[:end]
				}
				explore := []rankedCandidate{}
				inPool := map[string]bool{}
				for _, item := range pool {
					inPool[item.ModelID] = true
				}
				for _, item := range available {
					if !inPool[item.ModelID] || tier == "speed" && item.speed.LowConfidence {
						explore = append(explore, item)
					}
				}
				if len(explore) > 0 && draw(100) < 5 {
					chosen = rotateCandidate(explore, draw)
					reason = "exploration"
				} else {
					chosen = pool[draw(len(pool))]
				}
			}
		}
		if reason != "recommended" {
			chosen.attempt.Explorations++
		}
		chosen.attempt.LastAttempt = now
		if chosen.attempt.Attempts == 0 {
			chosen.attempt.PendingID = uuid.NewString()
			chosen.attempt.PendingAt = now
		}
		if err := tx.Save(&chosen.attempt).Error; err != nil {
			return err
		}
		result = SelectionDecision{SelectionCandidate: chosen.SelectionCandidate, Reason: reason, LowConfidence: chosen.speed.LowConfidence, SampleCount: chosen.speed.Count}
		if tier == "quality" {
			result.LowConfidence = chosen.quality.Count < 3
			result.SampleCount = chosen.quality.Count
		}
		if reason == "first_use" || reason == "no_data" {
			result.LowConfidence = true
		}
		return nil
	})
	return result, err
}

func rotateCandidate(items []rankedCandidate, draw func(int) int) rankedCandidate {
	// Shuffle each least-used round; oldest selection wins after the counts tie.
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].attempt.Explorations != items[j].attempt.Explorations {
			return items[i].attempt.Explorations < items[j].attempt.Explorations
		}
		return items[i].attempt.LastAttempt.Before(items[j].attempt.LastAttempt)
	})
	end := 1
	for end < len(items) && items[end].attempt.Explorations == items[0].attempt.Explorations && items[end].attempt.LastAttempt.Equal(items[0].attempt.LastAttempt) {
		end++
	}
	return items[draw(end)]
}
