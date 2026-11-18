package store

import (
	"context"

	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"

	"gorm.io/gorm"
)

// Preferences belongs to a persistent local identity, not a browser or session.
type Preferences struct {
	UserID string `json:"userId"`

	ManualModelKey     string            `json:"manualModelKey"`
	ReasoningByModelID map[string]string `json:"reasoningByModelId"`

	ShowUnrecognizedModels bool `json:"showUnrecognizedModels"`
}

type PreferencesPatch struct {
	ManualModelKey     *string           `json:"manualModelKey"`
	ReasoningByModelID map[string]string `json:"reasoningByModelId"`

	ShowUnrecognizedModels *bool `json:"showUnrecognizedModels"`
}

type preferenceRow struct {
	ID     string `gorm:"primaryKey"`
	UserID string `gorm:"uniqueIndex;not null"`

	ManualModelKey string
	ReasoningJSON  string

	ShowUnrecognizedModels bool `gorm:"not null;default:false"`
}

func (preferenceRow) TableName() string { return "model_selection_preferences" }

type runSelectionRow struct {
	RunID           string `gorm:"primaryKey"`
	ModelID         string
	ReasoningEffort string
	Endpoint        string
}

type selectionQueuedRow struct {
	Debug bool `gorm:"column:debug;not null;default:0"`
}

func (selectionQueuedRow) TableName() string { return "queued_inputs" }
func (runSelectionRow) TableName() string    { return "run_model_selections" }

type SessionSelectionPatch struct {
	ManualModelKey        *string `json:"manualModelKey"`
	ManualReasoningEffort *string `json:"manualReasoningEffort"`
	ProcessingTier        *string `json:"processingTier"`
}

func (s *Store) initModelSelectionSchema() error {
	// Add session settings and read position through GORM.
	for _, field := range []string{"ManualModelKey", "ManualReasoningEffort", "ProcessingTier", "InputQueuePaused", "ReadSequence", "Pinned"} {
		if !s.orm.Migrator().HasColumn(&sessionRow{}, field) {
			if err := s.orm.Migrator().AddColumn(&sessionRow{}, field); err != nil {
				return err
			}
		}
	}
	for _, field := range []string{"Debug"} {
		if !s.orm.Migrator().HasColumn(&selectionQueuedRow{}, field) {
			if err := s.orm.Migrator().AddColumn(&selectionQueuedRow{}, field); err != nil {
				return err
			}
		}
	}
	// Retain old tables for historical compatibility; this migration only
	// changes the settings of sessions that previously used automatic selection.
	if err := s.orm.AutoMigrate(&preferenceRow{}, &runSelectionRow{}); err != nil {
		return err
	}
	if !s.orm.Migrator().HasColumn(&sessionRow{}, "selection_mode") {
		return nil
	}
	effort := "''"
	if s.orm.Migrator().HasColumn(&sessionRow{}, "selection_reasoning_effort") {
		effort = "selection_reasoning_effort"
	}
	return s.orm.Transaction(func(tx *gorm.DB) error {
		return tx.Exec("UPDATE sessions SET manual_model_key=model_key, manual_reasoning_effort=" + effort + ", selection_mode='manual' WHERE selection_mode='auto'").Error
	})
}

func preferences(tx *gorm.DB) (Preferences, error) {
	row := preferenceRow{ID: "local", UserID: uuid.NewString(), ReasoningJSON: "{}"}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return Preferences{}, err
	}
	if err := tx.Where("id = ?", "local").Take(&row).Error; err != nil {
		return Preferences{}, err
	}
	result := Preferences{UserID: row.UserID, ManualModelKey: row.ManualModelKey, ShowUnrecognizedModels: row.ShowUnrecognizedModels, ReasoningByModelID: map[string]string{}}
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
		if patch.ManualModelKey != nil {
			result.ManualModelKey = *patch.ManualModelKey
		}
		if patch.ShowUnrecognizedModels != nil {
			result.ShowUnrecognizedModels = *patch.ShowUnrecognizedModels
		}
		for id, value := range patch.ReasoningByModelID {
			result.ReasoningByModelID[id] = value
		}
		encoded, err := json.Marshal(result.ReasoningByModelID)
		if err != nil {
			return err
		}
		return tx.Model(&preferenceRow{}).Where("id = ?", "local").Updates(map[string]any{
			"manual_model_key":         result.ManualModelKey,
			"reasoning_json":           string(encoded),
			"show_unrecognized_models": result.ShowUnrecognizedModels,
		}).Error
	})
	return result, err
}

func (s *Store) PatchSessionSelection(ctx context.Context, id string, patch SessionSelectionPatch) (Session, error) {
	err := s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current sessionRow
		if err := tx.Where("id=?", id).Take(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		values := map[string]any{}
		if patch.ManualModelKey != nil {
			values["manual_model_key"] = *patch.ManualModelKey
			if *patch.ManualModelKey != current.ManualModelKey {
				values["processing_tier"] = ""
				values["manual_reasoning_effort"] = ""
			}
		}
		if patch.ManualReasoningEffort != nil {
			values["manual_reasoning_effort"] = *patch.ManualReasoningEffort
		}
		if patch.ProcessingTier != nil {
			values["processing_tier"] = *patch.ProcessingTier
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

// RecordRunSelection supports restoration of legacy reasoning metadata.
func (s *Store) RecordRunSelection(ctx context.Context, runID, modelID, effort, endpoint string) error {
	if runID == "" || modelID == "" {
		return errors.New("missing run model identity")
	}
	return s.orm.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&runSelectionRow{RunID: runID, ModelID: modelID, ReasoningEffort: effort, Endpoint: endpoint}).Error
}
