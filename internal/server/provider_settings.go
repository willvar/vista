package server

import (
	"errors"
	"net/http"
	"vista/internal/modelcatalog"
)

type providerSettingsInput = modelcatalog.ConnectionInput

func (a *App) providerSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.Header().Set("Cache-Control", "no-store")
		jsonOut(w, modelcatalog.ProviderSetups())
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		settingsHTTPError(w, errors.New("method"), http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var input providerSettingsInput
	if err := decodeSettingsRequest(r.Body, &input); err != nil {
		settingsHTTPError(w, errors.New("invalid provider settings request"), http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodPost && input.ProviderID == "openai-codex" {
		settingsHTTPError(w, errors.New("sign in with ChatGPT to connect OpenAI Codex"), http.StatusBadRequest)
		return
	}
	if err := a.changeProviderSettings(r.Context(), r.Method, input); err != nil {
		settingsHTTPError(w, err, http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
