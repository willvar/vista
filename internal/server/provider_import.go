package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"vista/internal/codexauth"
	"vista/internal/modelcatalog"
)

func (a *App) importProviders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		settingsHTTPError(w, errors.New("method"), http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var input struct {
		Source         string   `json:"source"`
		Action         string   `json:"action"`
		ConfigPath     string   `json:"configPath"`
		CredentialPath string   `json:"credentialPath"`
		Fingerprint    string   `json:"fingerprint"`
		IDs            []string `json:"ids"`
	}
	if decodeSettingsRequest(r.Body, &input) != nil || input.Source != "opencode" || input.Action != "preview" && input.Action != "import" || len(input.ConfigPath) > 4096 || len(input.CredentialPath) > 4096 || len(input.IDs) > 16 {
		settingsHTTPError(w, errors.New("invalid provider import request"), http.StatusBadRequest)
		return
	}
	snapshot, err := a.modelSettingsSnapshot()
	if err != nil {
		settingsHTTPError(w, err, http.StatusInternalServerError)
		return
	}
	preview, err := modelcatalog.ReadOpenCodeImport(r.Context(), input.ConfigPath, input.CredentialPath)
	if err != nil {
		settingsHTTPError(w, err, http.StatusBadRequest)
		return
	}
	preview.MarkExisting(snapshot.raw)
	if input.Action == "preview" {
		jsonOut(w, preview)
		return
	}
	if input.Fingerprint == "" || input.Fingerprint != preview.Fingerprint {
		settingsHTTPError(w, modelcatalog.NewSettingsError("settings_conflict", "OpenCode configuration changed. Read it again before importing.", nil), http.StatusConflict)
		return
	}
	selected := map[string]bool{}
	for _, id := range input.IDs {
		if selected[id] || id == "" {
			settingsHTTPError(w, errors.New("choose unique connections to import"), http.StatusBadRequest)
			return
		}
		selected[id] = true
	}
	if len(selected) == 0 {
		settingsHTTPError(w, errors.New("choose at least one connection to import"), http.StatusBadRequest)
		return
	}
	groups := map[string]bool{}
	for _, entry := range preview.Connections {
		if !selected[entry.ID] || entry.CredentialGroup == "" {
			continue
		}
		if groups[entry.CredentialGroup] {
			settingsHTTPError(w, errors.New("choose only one credential source per connection"), http.StatusBadRequest)
			return
		}
		groups[entry.CredentialGroup] = true
	}
	type savedCredential struct {
		path       string
		credential codexauth.Credential
	}
	var credentials []savedCredential
	raw := snapshot.raw
	imported := 0
	for _, entry := range preview.Connections {
		if !selected[entry.ID] {
			continue
		}
		if entry.Reason != "" {
			settingsHTTPError(w, modelcatalog.NewSettingsError("settings_conflict", "A selected connection is no longer importable. Read the configuration again.", nil), http.StatusConflict)
			return
		}
		raw, err = modelcatalog.ChangeConnection(r.Context(), raw, snapshot.exists, http.MethodPost, entry.Input)
		if err != nil {
			settingsHTTPError(w, fmt.Errorf("could not import %s: %w", entry.Name, err), http.StatusBadRequest)
			return
		}
		if len(raw.Models) > 16 {
			settingsHTTPError(w, errors.New("import exceeds the saved model limit; select fewer connections or models"), http.StatusBadRequest)
			return
		}
		if entry.Credential != nil {
			credentials = append(credentials, savedCredential{codexauth.Path(a.configDir, raw.Models[len(raw.Models)-1].ConnectionID), *entry.Credential})
		}
		imported++
	}
	if imported != len(selected) {
		settingsHTTPError(w, errors.New("selected connection was not found in OpenCode"), http.StatusBadRequest)
		return
	}
	// All connections are staged first. Credentials and models publish together;
	// a conflict or failed save removes only newly created credential files.
	a.modelMu.Lock()
	defer a.modelMu.Unlock()
	if a.modelRevision != snapshot.revision {
		settingsHTTPError(w, modelcatalog.NewSettingsError("settings_conflict", "Model connections changed. Read the configuration again.", nil), http.StatusConflict)
		return
	}
	saved := []string{}
	committed := false
	defer func() {
		if committed {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, path := range saved {
			_ = codexauth.Remove(ctx, path)
		}
	}()
	for _, item := range credentials {
		if err := codexauth.Save(r.Context(), item.path, item.credential); err != nil {
			settingsHTTPError(w, errors.New("could not save an imported Codex account"), http.StatusInternalServerError)
			return
		}
		saved = append(saved, item.path)
	}
	if err := a.commitModelSettingsLocked(r.Context(), snapshot, raw); err != nil {
		settingsHTTPError(w, err, http.StatusBadRequest)
		return
	}
	committed = true
	jsonOut(w, map[string]int{"imported": imported})
}
