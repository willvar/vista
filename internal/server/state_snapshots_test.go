package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"vista/internal/modeltrace"
	"vista/internal/workerproto"
)

func TestChangedSettingsPushPublicSnapshotsAndUnchangedTopicsStayEmpty(t *testing.T) {
	consumer, _ := newResultConsumerTest(t)
	app := &App{store: consumer.store, configDir: t.TempDir(), models: modelCatalog{Default: "model", Models: []configuredModel{{Key: "model", ID: "fixture", Provider: "openai-chat", APIKey: "model-secret", Headers: map[string]string{"X-Key": "header-secret"}}}},
		providerLogins: map[string]*providerLogin{"login": {ID: "login", Status: "connected", UserCode: "device-secret", VerificationURL: "https://auth.test/secret"}},
		mcpServers:     []workerproto.MCPServer{{ID: "mcp", Name: "Tools", Transport: "http", Endpoint: "https://mcp.test", Token: "mcp-secret", ClientSecret: "client-secret", Headers: map[string]string{"X-Private": "mcp-header-secret"}}},
		mcpLogins:      map[string]*mcpLogin{"mcp-login": {ID: "mcp-login", Status: "pending", AuthorizationURL: "https://mcp.test/authorization-secret"}},
		storageStatus:  resourceStorageStatus{Phase: "running", Action: "clean", DeletedCount: 2, DeletedBytes: 100}}
	before := app.updates.snapshot()
	w := httptest.NewRecorder()
	app.memories(w, httptest.NewRequest("POST", "/api/memories", strings.NewReader(`{"action":"add","text":"Use concise answers."}`)))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	app.updates.notify("models", "catalog", "mcp", "provider-login", "storage")
	point, err := app.stateUpdate(consumer.ctx, "localhost", before)
	if err != nil {
		t.Fatal(err)
	}
	if point.Models == nil || point.Models.Default != "model" || len(point.Models.Models) != 1 || point.Catalog == nil || point.ProviderLogins == nil || len(*point.ProviderLogins) != 1 || point.MCP == nil || len(point.MCP.Servers) != 1 || len(point.MCP.Logins) != 1 || point.Storage == nil || point.Storage.DeletedCount != 2 || point.Memories == nil || len(point.Memories.Memories) != 1 {
		t.Fatalf("missing changed state: %+v", point)
	}
	data, _ := json.Marshal(point)
	for _, secret := range []string{"model-secret", "header-secret", "device-secret", "https://auth.test/secret", "mcp-secret", "client-secret", "mcp-header-secret", "authorization-secret"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("shared state exposed %q", secret)
		}
	}
	if !point.MCP.Servers[0].HasToken || !point.MCP.Servers[0].HasClientSecret || len(point.MCP.Servers[0].HeaderNames) != 1 {
		t.Fatal("public credential flags were lost")
	}
	next, err := app.stateUpdate(consumer.ctx, "localhost", point)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(next)
	var fields map[string]any
	_ = json.Unmarshal(data, &fields)
	if len(fields) != 2 {
		t.Fatalf("unchanged heartbeat repeated state: %s", data)
	}
}

func TestWorkerMemoryChangeNotifiesOnlyAfterCommit(t *testing.T) {
	consumer, _ := newResultConsumerTest(t)
	app := &App{store: consumer.store}
	consumer.changed = app.notifyWorkerState
	change := workerproto.MemoryChange{Action: "add", ID: uuid.NewString(), Text: "Prefer examples."}
	result := workerproto.Result{Kind: "memory_change", RunID: "run", Memory: &change, AckID: "ack"}
	if err := consumer.consume(result); err != nil {
		t.Fatal(err)
	}
	point, err := app.stateUpdate(consumer.ctx, "localhost", stateCheckpoint{Revisions: map[string]uint64{}})
	if err != nil || point.Memories == nil || point.Memories.Memories[0].Text != change.Text || point.Revisions["memories"] != 1 || point.Sessions != nil || point.Status != nil {
		t.Fatalf("memory commit was not isolated: %+v %v", point, err)
	}
	if err := consumer.consume(result); err != nil {
		t.Fatal(err)
	}
	if app.updates.snapshot().Revisions["memories"] != 1 {
		t.Fatal("rejected duplicate published a change")
	}
}

func TestStateBudgetDropsOnlyOversizedPayloads(t *testing.T) {
	models := &publicModelCatalog{Models: []modelInfo{{Key: "huge", ID: strings.Repeat("x", maxStateSnapshotBytes)}}}
	storage := &resourceStorageStatus{Phase: "idle"}
	point := stateCheckpoint{Epoch: "test", Revisions: map[string]uint64{"models": 1, "storage": 1}, Models: models, Storage: storage}
	if err := point.limitPayload(); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(point)
	if len(data) > maxStateSnapshotBytes || point.Models != nil || point.Storage != storage || point.Revisions["models"] != 1 {
		t.Fatalf("large model list discarded unrelated state: %d %+v", len(data), point)
	}
}

func TestDiagnosticNotificationsRequireSavedSummaryAndKeepBodiesOutOfState(t *testing.T) {
	consumer, _ := newResultConsumerTest(t)
	consumer.sessionID = uuid.NewString()
	runID := uuid.NewString()
	if _, err := consumer.store.Create(consumer.ctx, consumer.sessionID, "diagnostics"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := consumer.store.BeginRun(consumer.ctx, consumer.sessionID, runID, uuid.NewString(), "input"); err != nil {
		t.Fatal(err)
	}
	consumer.stateDir = t.TempDir()
	var hub stateHub
	consumer.diagnostic = hub.diagnosticSaved
	record := modeltrace.Record{Scope: modeltrace.Scope{SessionID: consumer.sessionID, RunID: runID, Turn: 1}, ID: uuid.NewString(), StartedAt: time.Now().UTC(), Model: "fixture", Exchanges: []*modeltrace.Exchange{{Response: modeltrace.Body{Content: "large-private-body"}}}}
	result := workerproto.Result{RunID: runID, Kind: "model_diagnostic", DiagnosticID: record.ID}
	if err := consumer.consume(result); err != nil {
		t.Fatal(err)
	}
	if hub.snapshot().Revisions["diagnostics"] != 0 {
		t.Fatal("notification preceded file save")
	}
	if err := modeltrace.Save(consumer.stateDir, record); err != nil {
		t.Fatal(err)
	}
	if err := consumer.consume(result); err != nil {
		t.Fatal(err)
	}
	changes := hub.diagnosticChanges(0, 1)
	if changes == nil || len(changes.Records) != 1 || changes.Records[0].ID != record.ID || changes.Through != 1 {
		t.Fatalf("missing saved index: %+v", changes)
	}
	data, _ := json.Marshal(changes)
	if strings.Contains(string(data), "large-private-body") || strings.Contains(string(data), "exchanges") {
		t.Fatal("diagnostic bodies pushed with index")
	}
	for range 256 {
		hub.diagnosticSaved(record.Summary())
	}
	if len(hub.diagnostics) != 256 || hub.diagnosticChanges(0, 257) != nil || hub.diagnosticChanges(1, 257) == nil {
		t.Fatal("bounded index cannot recover a delivery gap")
	}
}
