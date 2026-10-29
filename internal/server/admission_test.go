package server

import (
	"testing"
	"vista/internal/workerproto"
)

func TestAdmissionUsesMeasuredMemoryWithoutSessionCountLimit(t *testing.T) {
	config := memoryConfig{1024, 768, 2}
	if err := admitMemory(8<<30, 1023<<20, config); err != nil {
		t.Fatal(err)
	}
	if admitMemory(8<<30, 1024<<20, config) == nil {
		t.Fatal("exhausted budget accepted")
	}
	if admitMemory(minimumAvailableMemory-1, 0, config) == nil {
		t.Fatal("exhausted host accepted")
	}
	app := &App{memoryConfig: config, workers: map[string]*worker{}}
	for i := 0; i < 100; i++ {
		app.workers[string(rune(i))] = &worker{metrics: workerproto.Metrics{RSSBytes: 1 << 20}}
	}
	app.mu.Lock()
	err := app.admitWorkerLocked(8 << 30)
	app.mu.Unlock()
	if err != nil {
		t.Fatalf("idle conversations were capped by count: %v", err)
	}
	if app.admitRun(8<<30, 2) == nil {
		t.Fatal("configured execution concurrency ignored")
	}
}
func TestMemorySettingsPersistenceAndValidation(t *testing.T) {
	app := newSearchSettingsApp(t)
	if _, err := loadMemoryConfig(app.configDir); err != nil {
		t.Fatal(err)
	}
	for _, c := range []memoryConfig{{255, 128, 1}, {1024, 2048, 1}, {1024, 0, 1}, {1024, 768, 0}} {
		if validMemoryConfig(c) {
			t.Fatalf("invalid settings accepted: %+v", c)
		}
	}
}
