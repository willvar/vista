package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"vista/internal/workerproto"
)

type memoryConfig struct {
	BudgetMiB      int `json:"budgetMiB"`
	WorkerMiB      int `json:"workerMiB"`
	ConcurrentRuns int `json:"concurrentRuns"`
}

var defaultMemoryConfig = memoryConfig{2048, 1024, 4}

func validMemoryConfig(c memoryConfig) bool {
	return c.BudgetMiB >= 256 && c.BudgetMiB <= 1<<20 && c.WorkerMiB >= 128 && c.WorkerMiB <= c.BudgetMiB && c.ConcurrentRuns >= 1 && c.ConcurrentRuns <= 128
}
func loadMemoryConfig(dir string) (memoryConfig, error) {
	config := defaultMemoryConfig
	file, err := os.Open(filepath.Join(dir, "capacity.json"))
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return config, err
	}
	defer file.Close()
	if err = decodeSettingsRequest(io.LimitReader(file, 4097), &config); err != nil || !validMemoryConfig(config) {
		return memoryConfig{}, errors.New("invalid memory settings")
	}
	return config, nil
}
func (a *App) memorySettings() memoryConfig {
	a.memoryMu.Lock()
	defer a.memoryMu.Unlock()
	if a.memoryConfig.BudgetMiB == 0 {
		return defaultMemoryConfig
	}
	return a.memoryConfig
}
func (a *App) memorySettingsHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		jsonOut(w, a.memorySettings())
		return
	}
	if r.Method != http.MethodPut {
		http.Error(w, "method", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var input memoryConfig
	if decodeSettingsRequest(r.Body, &input) != nil || !validMemoryConfig(input) {
		http.Error(w, "budgetMiB: 256–1048576; workerMiB: 128–budgetMiB; concurrentRuns: 1–128", 400)
		return
	}
	a.memoryMu.Lock()
	file, err := os.CreateTemp(a.configDir, ".capacity-*")
	if err == nil {
		defer os.Remove(file.Name())
		defer file.Close()
		err = json.NewEncoder(file).Encode(input)
		if err == nil {
			err = file.Sync()
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(file.Name(), filepath.Join(a.configDir, "capacity.json"))
		}
	}
	if err == nil {
		a.memoryConfig = input
	}
	a.memoryMu.Unlock()
	if err != nil {
		http.Error(w, "could not save memory settings", 500)
		return
	}
	a.mu.Lock()
	workers := make([]*worker, 0, len(a.workers))
	for _, worker := range a.workers {
		workers = append(workers, worker)
	}
	a.mu.Unlock()
	for _, worker := range workers {
		_ = worker.send(workerproto.Command{Action: "limits", RunID: "limits", MemoryLimit: int64(input.WorkerMiB) << 20})
	}
	a.updates.notify("status")
	a.refreshMCPHost()
	jsonOut(w, input)
}
