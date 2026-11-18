package server

import (
	"encoding/json"
	"testing"

	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestModelAvailabilityAndCapacityWakeDispatchOneContinuation(t *testing.T) {
	a := selectionApp(t)
	a.ctx, a.configDir = t.Context(), t.TempDir()
	a.memoryConfig = defaultMemoryConfig
	a.memoryConfig.ConcurrentRuns = 1
	sink := &imageCommandSink{}
	a.workers = map[string]*worker{"resume": {input: sink}}
	defer a.background.Wait()
	ctx := a.ctx

	anchor := configuredModel{Key: "anchor", ID: "fixture-anchor", Provider: "openai-chat", ProviderID: "custom", Authentication: "none", BaseURL: "http://127.0.0.1:1/v1", InputModalities: []string{"text"}}
	model := anchor
	model.Key, model.ID = "discovered", "fixture-discovered"
	base := modelCatalog{Default: anchor.Key, Models: []configuredModel{anchor}}
	a.models, a.baseModels = base, base
	for _, id := range []string{"resume", "other"} {
		if _, err := a.store.Create(ctx, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.store.PatchSessionSelection(ctx, "resume", store.SessionSelectionPatch{ManualModelKey: &model.Key}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.store.BeginRun(ctx, "resume", "old-run", "request", "continue from saved input"); err != nil {
		t.Fatal(err)
	}
	if err := a.store.RecoverExecutions(ctx); err != nil {
		t.Fatal(err)
	}
	a.launchRuntimeContinuation("resume")
	if sink.Len() != 0 {
		t.Fatal("a missing explicitly selected model must not use the default")
	}
	if _, _, err := a.store.BeginRun(ctx, "other", "occupy", "other-request", "occupy the only slot"); err != nil {
		t.Fatal(err)
	}
	publish := func(next ...configuredModel) {
		a.modelMu.Lock()
		defer a.modelMu.Unlock()
		a.baseModels = modelCatalog{Default: anchor.Key, Models: next}
		a.rebuildModelsLocked()
	}
	publish(anchor, model)
	a.background.Wait()
	if pending, err := a.store.HasPendingRuntimeEvents(ctx, "resume"); err != nil || !pending || sink.Len() != 0 {
		t.Fatalf("readiness must respect admission and preserve pending work: pending=%t error=%v bytes=%d", pending, err, sink.Len())
	}

	publish(anchor)
	if _, err := a.store.FinishRun(ctx, "other", "occupy", "other-answer", "succeeded", "done"); err != nil {
		t.Fatal(err)
	}
	// Keep both scans waiting until publication and the ordinary capacity wake
	// have happened. Refreshing the same directory again must not duplicate work.
	a.admissionMu.Lock()
	publish(anchor, model)
	a.workerResultConsumer("other", &worker{}).released()
	publish(anchor, model)
	a.admissionMu.Unlock()
	a.background.Wait()

	var command workerproto.Command
	if err := json.Unmarshal(sink.Bytes(), &command); err != nil {
		t.Fatalf("expected exactly one Worker command after both wakeups: %v", err)
	}
	if command.Action != "resume" || command.Model.Key != model.Key || command.SessionID != "resume" || command.Prompt != "" || command.RunID == "" || command.RunID == "old-run" {
		t.Fatalf("invalid continuation: action=%q model=%q session=%q", command.Action, command.Model.Key, command.SessionID)
	}
}
