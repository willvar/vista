package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vista/internal/workerproto"
)

func TestMemorySettingsCommitUpdatesLiveWorkersAndSurvivesReload(t *testing.T) {
	app := newSearchSettingsApp(t)
	sink := &imageCommandSink{}
	app.workers = map[string]*worker{"session": {input: sink}}
	request := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		app.memorySettingsHTTP(w, httptest.NewRequest(http.MethodPut, "/api/memory-config", strings.NewReader(body)))
		return w
	}
	if w := request(`{"budgetMiB":1024,"workerMiB":768,"concurrentRuns":2}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	config, err := loadMemoryConfig(app.configDir)
	if err != nil || config != (memoryConfig{1024, 768, 2}) || app.memorySettings() != config {
		t.Fatalf("settings were not committed: %+v %v", config, err)
	}
	var command workerproto.Command
	if err := json.NewDecoder(sink).Decode(&command); err != nil || command.Action != "limits" || command.MemoryLimit != 768<<20 {
		t.Fatalf("resident Worker did not receive limit: %+v %v", command, err)
	}
	for _, body := range []string{`null`, `{}`, `{"budgetMiB":1024,"workerMiB":2048,"concurrentRuns":2}`, `{"budgetMiB":1024,"workerMiB":768,"concurrentRuns":0}`} {
		if w := request(body); w.Code != 400 || app.memorySettings() != config {
			t.Fatalf("invalid settings changed runtime: %d", w.Code)
		}
	}
	path := filepath.Join(app.configDir, "capacity.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if w := request(`{"budgetMiB":2048,"workerMiB":1024,"concurrentRuns":4}`); w.Code != 500 || app.memorySettings() != config {
		t.Fatal("failed write changed active settings")
	}
}

func TestMediaAdmissionRSSPreservesFullWorkerMetrics(t *testing.T) {
	w := &worker{metrics: workerproto.Metrics{RSSBytes: 10, PeakRSSBytes: 20, HeapBytes: 7, SampledAt: "earlier"}}
	app := &App{ctx: context.Background(), workers: map[string]*worker{"session": w}}
	app.recordWorkerMetrics("session", w, workerproto.Metrics{RSSBytes: 30})
	if w.metrics.HeapBytes != 7 || w.metrics.SampledAt != "earlier" || w.metrics.PeakRSSBytes != 30 || w.metrics.RSSBytes != 30 {
		t.Fatalf("admission destroyed full sample: %+v", w.metrics)
	}
}
