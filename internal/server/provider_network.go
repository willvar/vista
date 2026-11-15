package server

import (
	"errors"
	"net/http"

	"vista/internal/modelcatalog"
	"vista/internal/network"
)

// A network-only edit preserves advanced connection fields which the ordinary
// Provider form intentionally does not expose, including credentials and routes.
func (a *App) providerNetworkSettings(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var input struct {
		Key      string `json:"key"`
		ProxyID  string `json:"proxyId"`
		Revision string `json:"revision"`
	}
	if decodeSettingsRequest(r.Body, &input) != nil || input.Key == "" || len(input.Key) > 64 || len(input.ProxyID) > 80 {
		settingsHTTPError(w, errors.New("invalid provider network request"), 400)
		return
	}
	snapshot, err := a.modelSettingsSnapshot()
	if err != nil {
		settingsHTTPError(w, err, 500)
		return
	}
	if input.Revision == "" || input.Revision != a.modelSettingsVersion(snapshot.revision) {
		settingsHTTPError(w, errors.New("model settings changed; reload before saving"), 409)
		return
	}
	ctx, err := network.ForConnection(network.WithConfigDir(r.Context(), a.configDir), input.ProxyID)
	if err != nil {
		settingsHTTPError(w, err, 400)
		return
	}
	var previous *configuredModel
	for _, model := range snapshot.raw.Models {
		if model.Key == input.Key {
			copy := model
			previous = &copy
			break
		}
	}
	if previous == nil {
		settingsHTTPError(w, errors.New("model connection is no longer available"), 404)
		return
	}
	for index := range snapshot.raw.Models {
		if modelcatalog.SameConnection(*previous, snapshot.raw.Models[index]) {
			snapshot.raw.Models[index].ProxyID = input.ProxyID
		}
	}
	if err := a.commitModelSettings(ctx, snapshot, snapshot.raw); err != nil {
		status := 400
		var conflict *modelcatalog.SettingsError
		if errors.As(err, &conflict) && conflict.Code == "settings_conflict" {
			status = 409
		}
		settingsHTTPError(w, err, status)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
