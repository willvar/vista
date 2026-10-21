package server

import "time"

const restartWindow = time.Minute
const maxWorkerExitsPerWindow = 3

type workerFailureWindow struct {
	first time.Time
	count int
}

// recordWorkerFailureLocked is called with App.mu held. A successful but
// short-lived replacement counts as another failure in the same window.
func (a *App) recordWorkerFailureLocked(id string, now time.Time) bool {
	if a.workerFailures == nil {
		a.workerFailures = make(map[string]workerFailureWindow)
	}
	window := a.workerFailures[id]
	if window.first.IsZero() || now.Sub(window.first) >= restartWindow {
		window = workerFailureWindow{first: now}
	}
	if window.count < maxWorkerExitsPerWindow+1 {
		window.count++
	}
	a.workerFailures[id] = window
	return window.count <= maxWorkerExitsPerWindow
}
