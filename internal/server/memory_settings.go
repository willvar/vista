package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"vista/internal/configstore"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

type memoryConfig struct {
	BudgetMiB                int `json:"budgetMiB"`
	WorkerMiB                int `json:"workerMiB"`
	ConcurrentRuns           int `json:"concurrentRuns"`
	BackgroundJobsPerSession int `json:"backgroundJobsPerSession"`
}

// Missing limits retain the caller's defaults; explicit null is not a positive
// integer. The standard decoder otherwise treats null as an unchanged int.
func (c *memoryConfig) UnmarshalJSON(data []byte) error {
	type plain memoryConfig
	next := plain(*c)
	if err := decodeSettingsRequest(bytes.NewReader(data), &next); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, exists := fields["backgroundJobsPerSession"]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("backgroundJobsPerSession must be a positive integer")
	}
	*c = memoryConfig(next)
	return nil
}

var defaultMemoryConfig = memoryConfig{2048, 1024, 4, tools.DefaultBackgroundJobs}

func validMemoryConfig(c memoryConfig) bool {
	return c.BudgetMiB >= 256 && c.BudgetMiB <= 1<<20 && c.WorkerMiB >= 128 && c.WorkerMiB <= c.BudgetMiB && c.ConcurrentRuns >= 1 && c.ConcurrentRuns <= 128 && c.BackgroundJobsPerSession >= 1 && c.BackgroundJobsPerSession <= tools.MaxBackgroundJobs
}
func loadMemoryConfig(dir string) (memoryConfig, error) {
	config := defaultMemoryConfig
	data, exists, err := configstore.Read(dir, configstore.Capacity)
	if err != nil {
		return config, err
	}
	if !exists {
		return config, nil
	}
	if len(data) > 4096 || decodeSettingsRequest(bytes.NewReader(data), &config) != nil || !validMemoryConfig(config) {
		return memoryConfig{}, errors.New("invalid memory settings")
	}
	return config, nil
}
func (a *App) memorySettings() memoryConfig {
	if a.namedProfile {
		return a.profiles.device.memorySettings()
	}
	a.memoryMu.Lock()
	defer a.memoryMu.Unlock()
	if a.memoryConfig.BudgetMiB == 0 {
		return defaultMemoryConfig
	}
	return a.memoryConfig
}
func (a *App) memorySettingsHTTP(w http.ResponseWriter, r *http.Request) {
	if a.namedProfile {
		a.profiles.device.memorySettingsHTTP(w, r)
		return
	}
	if r.Method == http.MethodGet {
		jsonOut(w, a.memorySettings())
		return
	}
	if r.Method != http.MethodPut {
		http.Error(w, "method", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	input := memoryConfig{BackgroundJobsPerSession: a.memorySettings().BackgroundJobsPerSession}
	if decodeSettingsRequest(r.Body, &input) != nil || !validMemoryConfig(input) {
		http.Error(w, "budgetMiB: 256–1048576; workerMiB: 128–budgetMiB; concurrentRuns: 1–128; backgroundJobsPerSession: 1–1024", 400)
		return
	}
	a.workerLimitsMu.Lock()
	defer a.workerLimitsMu.Unlock()
	data, err := json.Marshal(input)
	if err != nil {
		http.Error(w, "could not save memory settings", 500)
		return
	}
	a.memoryMu.Lock()
	err = configstore.Write(a.configDir, configstore.Capacity, data)
	if err == nil {
		a.memoryConfig = input
	}
	a.memoryMu.Unlock()
	if err != nil {
		http.Error(w, "could not save memory settings", 500)
		return
	}
	list := []*App{a}
	if a.profiles != nil {
		list = a.profiles.scopedApps()
	}
	for _, app := range list {
		app.publishWorkerLimits(input)
	}
	jsonOut(w, input)
}

func (a *App) publishWorkerLimits(input memoryConfig) {
	a.mu.Lock()
	workers := make([]*worker, 0, len(a.workers))
	for _, worker := range a.workers {
		workers = append(workers, worker)
	}
	a.mu.Unlock()
	for _, worker := range workers {
		_ = worker.send(workerproto.Command{Action: "limits", RunID: "limits", MemoryLimit: int64(input.WorkerMiB) << 20, BackgroundJobsLimit: input.BackgroundJobsPerSession})
	}
	a.updates.notify("status")
	a.refreshMCPHost()
}
