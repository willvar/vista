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
	if r.Method == http.MethodPatch {
		a.providerNetworkSettings(w, r)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		settingsHTTPError(w, errors.New("method"), http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var input struct {
		modelcatalog.ConnectionInput
		SelectedProxy *string `json:"proxyId"`
	}
	if err := decodeSettingsRequest(r.Body, &input); err != nil {
		settingsHTTPError(w, errors.New("invalid provider settings request"), http.StatusBadRequest)
		return
	}
	if input.SelectedProxy != nil {
		input.ConnectionInput.ProxyID = *input.SelectedProxy
		input.ConnectionInput.ProxyIDSpecified = true
	}
	if r.Method == http.MethodPost && input.ProviderID == "openai-codex" {
		settingsHTTPError(w, errors.New("sign in with ChatGPT to connect OpenAI Codex"), http.StatusBadRequest)
		return
	}
	if err := a.changeProviderSettings(r.Context(), r.Method, input.ConnectionInput); err != nil {
		status := http.StatusBadRequest
		var conflict *modelcatalog.SettingsError
		if errors.As(err, &conflict) && conflict.Code == "settings_conflict" {
			status = http.StatusConflict
		}
		settingsHTTPError(w, err, status)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
