package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"vista/internal/modelcatalog"
	"vista/internal/provider"
)

func (a *App) ompCatalog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		jsonOut(w, a.modelCatalogInfo())
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	changed, err := a.checkModelCatalog(r.Context(), false)
	if err != nil {
		if errors.Is(err, errCatalogCheckInProgress) {
			settingsHTTPError(w, modelcatalog.NewSettingsError("catalog_update_in_progress", err.Error(), nil), http.StatusConflict)
			return
		}
		settingsHTTPError(w, modelcatalog.NewSettingsError("catalog_update_failed", err.Error(), nil), http.StatusBadGateway)
		return
	}
	jsonOut(w, struct {
		modelCatalogInfo
		Changed bool `json:"changed"`
	}{a.modelCatalogInfo(), changed})
}

type modelCatalogInfo struct {
	provider.OMPCatalogInfo
	catalogUpdateStatus
}

func (a *App) modelCatalogInfo() modelCatalogInfo {
	return modelCatalogInfo{provider.CurrentOMPCatalog().Info(), a.catalogUpdateStatus()}
}

func (a *App) checkModelCatalog(ctx context.Context, automatic bool) (changed bool, err error) {
	if !a.ompCatalogUpdateMu.TryLock() {
		return false, errCatalogCheckInProgress
	}
	defer a.ompCatalogUpdateMu.Unlock()
	a.ompCatalogStatusMu.Lock()
	a.ompCatalogStatus.Updating = true
	a.ompCatalogStatusMu.Unlock()
	defer func() { a.finishCatalogCheck(automatic, changed, err) }()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if a.ctx != nil {
		stop := context.AfterFunc(a.ctx, cancel)
		defer stop()
	}
	next, err := provider.FetchModelCatalog(ctx, a.ompCatalogClient)
	if err != nil {
		return false, err
	}
	current := provider.CurrentOMPCatalog().Info()
	changed = next.Info().Revision != current.Revision || next.Info().ModelsDevRevision != current.ModelsDevRevision
	if err := a.installOMPCatalog(ctx, next); err != nil {
		return false, err
	}
	// Re-read live inventories too: IDs previously excluded for missing metadata
	// can now become selectable. Existing account availability stays authoritative.
	a.refreshCustomModels(ctx)
	return changed, nil
}

func (a *App) installOMPCatalog(ctx context.Context, next *provider.OMPCatalogSnapshot) error {
	a.modelMu.Lock()
	defer a.modelMu.Unlock()
	current := provider.CurrentOMPCatalog().Info()
	changed := next.Info().Revision != current.Revision || next.Info().ModelsDevRevision != current.ModelsDevRevision
	if err := ctx.Err(); err != nil {
		return err
	}
	resolved, err := modelcatalog.LoadWithOMPCatalog(a.configDir, next)
	if err != nil {
		return fmt.Errorf("catalog update cannot load current model connections: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := next.Save(a.configDir); err != nil {
		return fmt.Errorf("could not save model catalog: %w", err)
	}
	next.Activate()
	if changed {
		a.publishBaseModelsLocked(resolved)
	}
	return nil
}
