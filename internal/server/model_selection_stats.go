package server

import (
	"net/http"
	"slices"

	"vista/internal/provider"
	"vista/internal/store"
)

type modelSelectionStatistics struct {
	store.ModelSelectionStatistics
	Providers []string `json:"providers"`
}

func (a *App) modelSelectionStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	prefs, err := a.store.Preferences(r.Context())
	if err != nil {
		http.Error(w, "could not read identity", http.StatusInternalServerError)
		return
	}
	if r.Method == http.MethodDelete {
		var input struct {
			Scope string `json:"scope"`
		}
		if !decodeSelectionBody(w, r, &input) {
			return
		}
		if input.Scope != "speed" && input.Scope != "quality" && input.Scope != "all" {
			http.Error(w, "scope must be speed, quality or all", http.StatusBadRequest)
			return
		}
		if err := a.store.ResetSelectionStatistics(r.Context(), prefs.UserID, input.Scope); err != nil {
			http.Error(w, "could not reset model statistics", http.StatusInternalServerError)
			return
		}
	}
	catalog := a.modelsSnapshot()
	candidates := []store.SelectionCandidate{}
	providers := map[string][]string{}
	seen := map[string]bool{}
	for _, entry := range catalog.Models {
		if !entry.Available() || entry.Tools != nil && !*entry.Tools {
			continue
		}
		model, key, err := catalog.Resolve(entry.Key)
		if err != nil || model.Provider == "" || model.ID == "" || highestSelectionReasoning(&model) != nil {
			continue
		}
		name := entry.Name
		if name == "" {
			name = entry.ProviderID
		}
		if name == "" {
			name = entry.Provider
		}
		if !slices.Contains(providers[model.ID], name) {
			providers[model.ID] = append(providers[model.ID], name)
		}
		if !seen[model.ID] {
			// Match selection's one ticket per model ID, using its first connection.
			seen[model.ID] = true
			candidates = append(candidates, store.SelectionCandidate{ModelID: model.ID, ModelKey: key, ReasoningEffort: provider.ModelSelectionEffort(model), Endpoint: provider.ModelEndpointID(model)})
		}
	}
	statistics, err := a.store.SelectionStatistics(r.Context(), prefs.UserID, candidates)
	if err != nil {
		http.Error(w, "could not read model statistics", http.StatusInternalServerError)
		return
	}
	models := make([]modelSelectionStatistics, 0, len(statistics))
	for _, item := range statistics {
		models = append(models, modelSelectionStatistics{ModelSelectionStatistics: item, Providers: providers[item.ModelID]})
	}
	jsonOut(w, map[string]any{"speedScope": "global", "qualityScope": "user", "speedWindowDays": 30, "qualityWindowSize": 200, "models": models})
}
