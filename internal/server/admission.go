package server

import (
	"errors"
	"fmt"

	"vista/internal/platform"
)

// Admission uses measured resident memory, not per-session predictions. These
// are soft guards; the OS remains the authority on process allocation/OOM.
const minimumAvailableMemory = 128 << 20

func admitMemory(available, used uint64, config memoryConfig) error {
	if available < minimumAvailableMemory {
		return fmt.Errorf("insufficient available system memory: %d MiB", available>>20)
	}
	if used >= uint64(config.BudgetMiB)<<20 {
		return fmt.Errorf("Vista memory budget reached: %d / %d MiB; wait for work to finish or increase the budget in About Vista", used>>20, config.BudgetMiB)
	}
	return nil
}

// Caller holds App.mu.
func (a *App) residentMemoryLocked() uint64 {
	used := platform.RSS()
	if a.mcpHost != nil {
		_, metrics := a.mcpHost.Status()
		used += metrics.RSSBytes
	}
	for _, worker := range a.workers {
		used += worker.metrics.RSSBytes
	}
	return used
}
func (a *App) admitWorkerLocked(available uint64) error {
	return admitMemory(available, a.residentMemoryLocked(), a.memorySettings())
}
func (a *App) admitRun(available uint64, running int) error {
	settings := a.memorySettings()
	if running >= settings.ConcurrentRuns {
		return errors.New("concurrent execution limit reached; wait or adjust memory settings in About Vista")
	}
	a.mu.Lock()
	used := a.residentMemoryLocked()
	a.mu.Unlock()
	return admitMemory(available, used, settings)
}
func (a *App) admitHeavy() error {
	available, err := platform.AvailableMemory()
	if err != nil {
		return fmt.Errorf("memory admission unavailable: %w", err)
	}
	a.mu.Lock()
	used := a.residentMemoryLocked()
	a.mu.Unlock()
	return admitMemory(available, used, a.memorySettings())
}
