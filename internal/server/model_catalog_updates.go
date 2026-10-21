package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"time"

	"vista/internal/provider"
)

const (
	catalogCheckInterval = 24 * time.Hour
	catalogCheckJitter   = 15 * time.Minute
	catalogRetryInterval = 6 * time.Hour
	catalogHistoryWindow = 7 * 24 * time.Hour
)

var errCatalogCheckInProgress = errors.New("a model catalog check is already in progress")

type catalogUpdateStatus struct {
	CheckedAt        time.Time            `json:"checkedAt,omitzero"`
	NextCheckAt      time.Time            `json:"nextCheckAt,omitzero"`
	CheckError       string               `json:"checkError,omitempty"`
	LastCheckChanged bool                 `json:"lastCheckChanged"`
	Updating         bool                 `json:"updating,omitempty"`
	History          []catalogCheckRecord `json:"history,omitempty"`
}

type catalogCheckRecord struct {
	provider.OMPCatalogInfo
	CheckedAt time.Time `json:"checkedAt"`
	Automatic bool      `json:"automatic"`
	Changed   bool      `json:"changed"`
	Error     string    `json:"error,omitempty"`
}

func recentCatalogChecks(history []catalogCheckRecord, now time.Time) []catalogCheckRecord {
	var recent []catalogCheckRecord
	for _, record := range history {
		if record.CheckedAt.After(now.Add(-catalogHistoryWindow)) {
			recent = append(recent, record)
		}
	}
	return recent
}

func catalogUpdateStatusPath(dir string) string {
	return filepath.Join(dir, "model-catalog-status.json")
}

func loadCatalogUpdateStatus(dir string) (catalogUpdateStatus, error) {
	var status catalogUpdateStatus
	data, err := os.ReadFile(catalogUpdateStatusPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return status, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &status)
	}
	status.Updating = false
	status.History = recentCatalogChecks(status.History, time.Now())
	return status, err
}

func saveCatalogUpdateStatus(dir string, status catalogUpdateStatus) error {
	status.Updating = false
	status.History = recentCatalogChecks(status.History, time.Now())
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".model-catalog-status-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), catalogUpdateStatusPath(dir))
}

func (a *App) initCatalogUpdates() {
	status, err := loadCatalogUpdateStatus(a.configDir)
	if err != nil {
		log.Printf("model catalog check status unavailable: %v", err)
		status = catalogUpdateStatus{}
	}
	a.ompCatalogStatus = status
	a.ompCatalogWake = make(chan struct{}, 1)
}

func (a *App) catalogUpdateStatus() catalogUpdateStatus {
	a.ompCatalogStatusMu.Lock()
	defer a.ompCatalogStatusMu.Unlock()
	status := a.ompCatalogStatus
	status.History = recentCatalogChecks(status.History, time.Now())
	return status
}

func (a *App) finishCatalogCheck(automatic, changed bool, checkErr error) {
	a.ompCatalogStatusMu.Lock()
	defer a.ompCatalogStatusMu.Unlock()
	// Interrupted shutdowns leave the previous deadline on disk so startup can
	// retry an overdue check, rather than delaying it for another six hours.
	if a.ctx != nil && a.ctx.Err() != nil {
		a.ompCatalogStatus.Updating = false
		return
	}
	now := time.Now().UTC()
	delay := catalogCheckInterval + time.Duration(rand.Int64N(int64(catalogCheckJitter)))
	status := catalogUpdateStatus{CheckedAt: now, NextCheckAt: now.Add(delay), LastCheckChanged: changed}
	if checkErr != nil {
		status.CheckError = checkErr.Error()
		status.NextCheckAt = now.Add(catalogRetryInterval)
		status.LastCheckChanged = false
	}
	record := catalogCheckRecord{OMPCatalogInfo: provider.CurrentOMPCatalog().Info(), CheckedAt: now,
		Automatic: automatic, Changed: status.LastCheckChanged, Error: status.CheckError}
	status.History = append([]catalogCheckRecord{record}, recentCatalogChecks(a.ompCatalogStatus.History, now)...)
	a.ompCatalogStatus = status
	if err := saveCatalogUpdateStatus(a.configDir, status); err != nil {
		log.Printf("save model catalog check status: %v", err)
	}
	select {
	case a.ompCatalogWake <- struct{}{}:
	default:
	}
}

func (a *App) pollModelCatalog(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		status := a.catalogUpdateStatus()
		if !status.Updating && !status.NextCheckAt.After(time.Now()) {
			if _, err := a.checkModelCatalog(ctx, true); err != nil && !errors.Is(err, errCatalogCheckInProgress) && ctx.Err() == nil {
				log.Printf("automatic model catalog check: %v", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-a.ompCatalogWake:
		}
	}
}
