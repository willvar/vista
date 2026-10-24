package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"vista/internal/modelcatalog"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/store"
)

func decodeSelectionBody(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid request body", 400)
		return false
	}
	return true
}

func validSelectionMode(mode string) bool { return mode == "manual" || mode == "auto" }
func validSelectionTier(tier string) bool { return tier == "speed" || tier == "quality" }

func (a *App) preferences(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		value, err := a.store.Preferences(r.Context())
		if err != nil {
			http.Error(w, "could not read preferences", 500)
			return
		}
		jsonOut(w, value)
	case http.MethodPatch:
		var patch store.PreferencesPatch
		if !decodeSelectionBody(w, r, &patch) {
			return
		}
		if patch.LastMode != nil && !validSelectionMode(*patch.LastMode) || patch.LastTier != nil && !validSelectionTier(*patch.LastTier) || patch.ManualModelKey != nil && len(*patch.ManualModelKey) > 512 {
			http.Error(w, "invalid preference", 400)
			return
		}
		if len(patch.ReasoningByModelID) > 20000 {
			http.Error(w, "too many model preferences", 400)
			return
		}
		for id, effort := range patch.ReasoningByModelID {
			if id == "" || len(id) > 512 || len(effort) > 32 {
				http.Error(w, "invalid model reasoning preference", 400)
				return
			}
		}
		value, err := a.store.PatchPreferences(r.Context(), patch)
		if err != nil {
			http.Error(w, "could not save preferences", 500)
			return
		}
		jsonOut(w, value)
	default:
		http.Error(w, "method", 405)
	}
}

func (a *App) initializeSessionSelection(ctx context.Context, id string) (store.Session, error) {
	prefs, err := a.store.Preferences(ctx)
	if err != nil {
		return store.Session{}, err
	}
	effort := ""
	key := ""
	catalog := a.modelsSnapshot()
	model, selected, resolveErr := catalog.Resolve(prefs.ManualModelKey)
	if resolveErr != nil {
		model, selected, resolveErr = catalog.Resolve("")
	}
	if resolveErr == nil {
		key = selected
		effort = restoredManualEffort(model, prefs.ReasoningByModelID[model.ID])
	}
	return a.store.PatchSessionSelection(ctx, id, store.SessionSelectionPatch{SelectionMode: &prefs.LastMode, SelectionTier: &prefs.LastTier, ManualModelKey: &key, ManualReasoningEffort: &effort})
}

func restoredManualEffort(model modelcfg.Model, effort string) string {
	options := provider.SessionReasoningOptions(model)
	if len(options) == 0 {
		return ""
	}
	if !slices.Contains(options, effort) {
		effort = options[0]
		if effort == "off" && len(options) > 1 {
			effort = options[1]
		}
	}
	if modelcatalog.ApplyReasoningEffort(&model, effort) != nil {
		return ""
	}
	return effort
}

func (a *App) sessionSelection(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPatch {
		http.Error(w, "method", 405)
		return
	}
	var patch store.SessionSelectionPatch
	if !decodeSelectionBody(w, r, &patch) {
		return
	}
	if patch.SelectionMode != nil && !validSelectionMode(*patch.SelectionMode) || patch.SelectionTier != nil && !validSelectionTier(*patch.SelectionTier) || patch.ManualModelKey != nil && len(*patch.ManualModelKey) > 512 || patch.ManualReasoningEffort != nil && len(*patch.ManualReasoningEffort) > 32 {
		http.Error(w, "invalid session selection", 400)
		return
	}
	value, err := a.store.PatchSessionSelection(r.Context(), id, patch)
	if err != nil {
		http.Error(w, "could not save session selection", 500)
		return
	}
	a.hub.notify(id)
	jsonOut(w, value)
}

func (a *App) sessionFeedback(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", 405)
		return
	}
	prefs, err := a.store.Preferences(r.Context())
	if err != nil {
		http.Error(w, "could not read identity", 500)
		return
	}
	feedback, err := a.store.SessionFeedback(r.Context(), prefs.UserID, id)
	if err != nil {
		http.Error(w, "could not read feedback", 500)
		return
	}
	jsonOut(w, map[string]any{"feedback": feedback})
}

func (a *App) messageFeedback(w http.ResponseWriter, r *http.Request, sessionID, messageID string) {
	if r.Method != http.MethodPut {
		http.Error(w, "method", 405)
		return
	}
	var input struct {
		Value json.RawMessage `json:"value"`
	}
	if !decodeSelectionBody(w, r, &input) {
		return
	}
	var feedbackValue *string
	if len(input.Value) == 0 || json.Unmarshal(input.Value, &feedbackValue) != nil {
		http.Error(w, "value must be up, down or null", 400)
		return
	}
	if feedbackValue != nil && *feedbackValue != "up" && *feedbackValue != "down" {
		http.Error(w, "value must be up, down or null", 400)
		return
	}
	prefs, err := a.store.Preferences(r.Context())
	if err != nil {
		http.Error(w, "could not read identity", 500)
		return
	}
	value, err := a.store.SetReplyFeedback(r.Context(), prefs.UserID, sessionID, messageID, feedbackValue)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if errors.Is(err, store.ErrFeedbackTarget) {
		http.Error(w, err.Error(), 400)
		return
	}
	if err != nil {
		http.Error(w, "could not save feedback", 500)
		return
	}
	a.hub.notify(sessionID)
	jsonOut(w, value)
}

type selectionInput struct {
	SelectionMode   string
	SelectionTier   string
	ModelKey        string
	ReasoningEffort string
	InputTokens     int64
	HasImages       bool
}

func highestSelectionReasoning(model *modelcfg.Model) error {
	options := provider.SessionReasoningOptions(*model)
	if len(options) == 0 {
		return nil
	}
	return modelcatalog.ApplyReasoningEffort(model, options[len(options)-1])
}

func (a *App) resolveSelection(ctx context.Context, sessionID string, input selectionInput) (modelcfg.Model, string, error) {
	session, err := a.store.Get(ctx, sessionID)
	if err != nil {
		return modelcfg.Model{}, "", err
	}
	mode, tier := input.SelectionMode, input.SelectionTier
	if mode == "" {
		mode = session.SelectionMode
	}
	if tier == "" {
		tier = session.SelectionTier
	}
	if !validSelectionMode(mode) || !validSelectionTier(tier) {
		return modelcfg.Model{}, "", errors.New("invalid selection mode or tier")
	}
	catalog := a.modelsSnapshot()
	if mode == "manual" {
		key := input.ModelKey
		if key == "" {
			key = session.ManualModelKey
		}
		if key == "" && session.SelectionMode == "manual" {
			key = session.ModelKey
		}
		model, key, err := a.resolveForRun(ctx, key)
		if err != nil {
			return model, key, err
		}
		model.Key = key
		effort := input.ReasoningEffort
		if effort == "" {
			if key == session.ManualModelKey {
				effort = session.ManualReasoningEffort
			}
			effort = restoredManualEffort(model, effort)
		}
		if err := modelcatalog.ApplyReasoningEffort(&model, effort); err != nil {
			return modelcfg.Model{}, "", err
		}
		_, err = a.store.PatchSessionSelection(ctx, sessionID, store.SessionSelectionPatch{SelectionMode: &mode, SelectionTier: &tier, ManualModelKey: &key, ManualReasoningEffort: &effort})
		return model, key, err
	}
	inputTokens, err := a.store.SelectionInputEstimate(ctx, sessionID, input.InputTokens, input.HasImages)
	if err != nil {
		return modelcfg.Model{}, "", err
	}
	candidates := []store.SelectionCandidate{}
	cooldowns, err := a.store.ConnectionCooldowns(ctx)
	if err != nil {
		return modelcfg.Model{}, "", err
	}
	for _, entry := range catalog.Models {
		if !entry.Available() {
			continue
		}
		if entry.Tools != nil && !*entry.Tools {
			continue
		}
		if input.HasImages && !slices.Contains(entry.EffectiveInputModalities(), "image") {
			continue
		}
		model, key, err := catalog.Resolve(entry.Key)
		if err != nil || model.Provider == "" || model.ID == "" {
			continue
		}
		if input.HasImages && !supportsImageInput(model.Provider) {
			continue
		}
		if err := highestSelectionReasoning(&model); err != nil {
			continue
		}
		if cooldowns[model.ID+"\x00"+provider.ModelEndpointID(model)] {
			continue
		}

		candidates = append(candidates, store.SelectionCandidate{ModelID: model.ID, ModelKey: key, ReasoningEffort: provider.ModelSelectionEffort(model), Endpoint: provider.ModelEndpointID(model)})
	}
	if len(candidates) == 0 {
		return modelcfg.Model{}, "", errors.New("no configured model supports this task's tools, input and context")
	}
	// Each automatic tier retains its own eligible model across tier and mode
	// switches. Reasoning uses today's maximum without changing that model.
	remembered, err := a.store.SessionTierSelection(ctx, sessionID, tier)
	if err != nil {
		return modelcfg.Model{}, "", err
	}
	if remembered != nil {
		for _, candidate := range candidates {
			if candidate.ModelKey == remembered.ModelKey {
				model, key, err := a.resolveForRun(ctx, candidate.ModelKey)
				if err != nil {
					break
				}
				model.Key = key
				if err := highestSelectionReasoning(&model); err != nil {
					return modelcfg.Model{}, "", err
				}
				err = a.store.BindSessionTierSelection(ctx, sessionID, tier, key, provider.ModelSelectionEffort(model))
				return model, key, err
			}
		}
	}
	prefs, err := a.store.Preferences(ctx)
	if err != nil {
		return modelcfg.Model{}, "", err
	}
	decision, err := a.store.SelectModel(ctx, prefs.UserID, tier, candidates, inputTokens)
	if err != nil {
		return modelcfg.Model{}, "", err
	}
	selectionBound := false
	defer func() {
		if !selectionBound {
			_ = a.store.CancelPendingSelection(context.Background(), prefs.UserID, decision.ModelID, decision.ReasoningEffort, "unavailable")
		}
	}()
	model, key, err := a.resolveForRun(ctx, decision.ModelKey)
	if err != nil {
		_ = a.store.FinishModelAttempt(ctx, prefs.UserID, decision.ModelID, decision.ReasoningEffort, "unavailable")
		_ = a.store.NoteConnectionFailure(ctx, decision.ModelID, decision.Endpoint)
		return model, key, err
	}
	model.Key = key
	if err := highestSelectionReasoning(&model); err != nil {
		return modelcfg.Model{}, "", err
	}
	if err := a.store.BindSessionTierSelection(ctx, sessionID, tier, key, provider.ModelSelectionEffort(model)); err != nil {
		return modelcfg.Model{}, "", err
	}
	selectionBound = true
	return model, key, nil
}

func (a *App) finishUnobservedSelection(ctx context.Context, model modelcfg.Model, outcome string) {
	if model.ID == "" {
		return
	}
	prefs, err := a.store.Preferences(ctx)
	if err != nil {
		return
	}
	_ = a.store.CancelPendingSelection(ctx, prefs.UserID, model.ID, provider.ModelSelectionEffort(model), outcome)
}
