package server

import (
	"errors"
	"net/http"

	"vista/internal/modelcatalog"
)

// Patch one connection setting without replacing credentials or advanced fields.
func (a *App) patchProviderSettings(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var input struct {
		Key      string  `json:"key"`
		ProxyID  *string `json:"proxyId"`
		Disabled *bool   `json:"disabled"`
		Revision string  `json:"revision"`
	}
	if decodeSettingsRequest(r.Body, &input) != nil || input.Key == "" || len(input.Key) > 64 ||
		(input.ProxyID == nil) == (input.Disabled == nil) || input.ProxyID != nil && len(*input.ProxyID) > 80 {
		settingsHTTPError(w, errors.New("invalid provider settings patch"), 400)
		return
	}
	snapshot, err := a.modelSettingsSnapshot()
	if err != nil {
		settingsHTTPError(w, err, 500)
		return
	}
	if input.Revision == "" || input.Revision != a.modelSettingsVersion(snapshot.revision) {
		settingsHTTPError(w, modelcatalog.NewSettingsError("settings_conflict", "Model settings changed; reload before saving.", nil), 409)
		return
	}
	if input.Disabled != nil && !snapshot.exists {
		settingsHTTPError(w, errors.New("only saved model connections can be enabled or disabled"), 400)
		return
	}
	var anchor *configuredModel
	for _, model := range snapshot.raw.Models {
		if model.Key == input.Key {
			anchor = &model
			break
		}
	}
	if anchor == nil {
		settingsHTTPError(w, errors.New("model connection is no longer available"), 404)
		return
	}
	for index := range snapshot.raw.Models {
		model := &snapshot.raw.Models[index]
		if !modelcatalog.SameConnection(*anchor, *model) {
			continue
		}
		if input.Disabled != nil {
			model.Disabled = *input.Disabled
		} else {
			model.ProxyID = *input.ProxyID
		}
	}
	a.modelMu.Lock()
	err = a.commitModelSettingsLocked(r.Context(), snapshot, snapshot.raw, input.ProxyID != nil)
	a.modelMu.Unlock()
	if err != nil {
		settingsHTTPError(w, err, 400)
		return
	}
	if input.Disabled != nil {
		a.writeModelSettings(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
