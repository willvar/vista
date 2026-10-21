package server

import (
	"fmt"

	"vista/internal/workerproto"
)

// Soft reservations include room for bursts; they are not OS hard limits.
// Calibrate these values with the 8 GiB / 20 Session release baseline.
const systemReserve = 512 << 20
const workerReserve = 96 << 20
const runReserve = 64 << 20
const workerHeapSoft = 160 << 20
const workerRSSSoft = 256 << 20
const maxWorkers = 64
const maxConcurrentRuns = 20

func admitWorker(available uint64, workers int) error {
	if workers >= maxWorkers {
		return fmt.Errorf("Worker limit of %d reached", maxWorkers)
	}
	required := uint64(systemReserve) + uint64(workers+1)*uint64(workerReserve)
	if available < required {
		return fmt.Errorf("insufficient available memory to open Session: %d MiB available, %d MiB reserved", available>>20, required>>20)
	}
	return nil
}

func admitRun(available uint64, running int) error {
	if running >= maxConcurrentRuns {
		return fmt.Errorf("concurrent execution limit of %d reached", maxConcurrentRuns)
	}
	required := uint64(systemReserve) + uint64(running+1)*uint64(runReserve)
	if available < required {
		return fmt.Errorf("insufficient available memory to execute: %d MiB available, %d MiB reserved", available>>20, required>>20)
	}
	return nil
}
func overWorkerSoftLimit(metrics workerproto.Metrics) bool {
	return metrics.HeapBytes > workerHeapSoft || metrics.RSSBytes > workerRSSSoft
}
