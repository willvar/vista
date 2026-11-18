package server

import (
	"context"
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
		if patch.ManualModelKey != nil && len(*patch.ManualModelKey) > 512 {
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
		if patch.ShowUnrecognizedModels != nil {
			a.updates.notify("models")
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
	preferred := visibleModelKey(catalog, prefs.ManualModelKey, prefs.ShowUnrecognizedModels)
	model, selected, resolveErr := catalog.Resolve(preferred)
	if preferred == "" || resolveErr != nil {
		preferred = initialVisibleModelKey(catalog, prefs.ShowUnrecognizedModels)
		if preferred != "" {
			model, selected, resolveErr = catalog.Resolve(preferred)
		}
	}
	if resolveErr == nil && preferred != "" {
		key = selected
		effort = restoredManualEffort(model, prefs.ReasoningByModelID[model.ID])
	}
	return a.store.PatchSessionSelection(ctx, id, store.SessionSelectionPatch{ManualModelKey: &key, ManualReasoningEffort: &effort})
}

func configuredChatKey(catalog modelCatalog, showUnrecognized bool) string {
	if catalog.Default != "" {
		for _, row := range catalog.Models {
			if row.Key == catalog.Default && row.Available() && (showUnrecognized || row.Recognized()) {
				return row.Key
			}
		}
		return ""
	}
	return initialVisibleModelKey(catalog, showUnrecognized)
}

func initialVisibleModelKey(catalog modelCatalog, showUnrecognized bool) string {
	first := ""
	for _, model := range catalog.Models {
		if !model.Available() || !showUnrecognized && !model.Recognized() {
			continue
		}
		if first != "" {
			return ""
		}
		first = model.Key
	}
	return first
}

// Never replace a saved or explicit choice with a different connection.
func visibleModelKey(catalog modelCatalog, key string, showUnrecognized bool) string {
	if key == "" {
		return initialVisibleModelKey(catalog, showUnrecognized)
	}
	for _, model := range catalog.Models {
		if model.Key == key && (!model.Available() || !showUnrecognized && !model.Recognized()) {
			return ""
		}
	}
	return key
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
	if patch.ManualModelKey != nil && len(*patch.ManualModelKey) > 512 || patch.ManualReasoningEffort != nil && len(*patch.ManualReasoningEffort) > 32 {
		http.Error(w, "invalid session selection", 400)
		return
	}
	hasExplicitModel := patch.ManualModelKey != nil && *patch.ManualModelKey != ""
	hasTier := patch.ProcessingTier != nil && *patch.ProcessingTier != ""
	if hasTier && !hasExplicitModel {
		http.Error(w, "a processing tier requires an explicit model", 400)
		return
	}
	if hasExplicitModel {
		model, _, err := a.modelsSnapshot().ResolveEnabled(*patch.ManualModelKey)
		if err == nil && hasTier {
			model.Key = *patch.ManualModelKey
			model = a.processingTierMetadata(r.Context(), model)
			err = provider.ApplyProcessingTier(&model, *patch.ProcessingTier)
		}
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	}
	value, err := a.store.PatchSessionSelection(r.Context(), id, patch)
	if err != nil {
		http.Error(w, "could not save session selection", 500)
		return
	}
	a.notifySession(id, "sessions")
	jsonOut(w, value)
}

type selectionInput struct {
	ModelKey        string
	ReasoningEffort string
	ProcessingTier  *string // Nil inherits only for the same selected model.
}

func applyManualSelection(model *modelcfg.Model, key string, session store.Session, input selectionInput) (string, error) {
	effort, tier := input.ReasoningEffort, ""
	if key == session.ManualModelKey {
		tier = session.ProcessingTier
	}
	if input.ProcessingTier != nil {
		tier = *input.ProcessingTier
	}
	if effort == "" {
		if key == session.ManualModelKey {
			effort = session.ManualReasoningEffort
		}
		effort = restoredManualEffort(*model, effort)
	}
	if err := modelcatalog.ApplyReasoningEffort(model, effort); err != nil {
		return "", err
	}
	return effort, provider.ApplyProcessingTier(model, tier)
}

func (a *App) resolveSelection(ctx context.Context, sessionID string, input selectionInput) (modelcfg.Model, string, error) {
	session, err := a.store.Get(ctx, sessionID)
	if err != nil {
		return modelcfg.Model{}, "", err
	}
	key := input.ModelKey
	if key == "" {
		key = session.ManualModelKey
	}
	prefs, err := a.store.Preferences(ctx)
	if err != nil {
		return modelcfg.Model{}, "", err
	}
	key = visibleModelKey(a.modelsSnapshot(), key, prefs.ShowUnrecognizedModels)
	if key == "" {
		return modelcfg.Model{}, "", errors.New("Choose an available conversation model in Settings")
	}
	model, key, err := a.resolveForRun(ctx, key)
	if err != nil {
		return model, key, err
	}
	model.Key = key
	if input.ProcessingTier != nil && *input.ProcessingTier != "" || session.ManualModelKey == key && session.ProcessingTier != "" {
		model = a.processingTierMetadata(ctx, model)
	}
	effort, err := applyManualSelection(&model, key, session, input)
	if err != nil {
		return modelcfg.Model{}, "", err
	}
	_, err = a.store.PatchSessionSelection(ctx, sessionID, store.SessionSelectionPatch{ManualModelKey: &key, ManualReasoningEffort: &effort, ProcessingTier: &model.ProcessingTier})
	return model, key, err
}
