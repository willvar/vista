package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vista/internal/configstore"
	"vista/internal/resources"
	"vista/internal/resourcetest"
	"vista/internal/store"
)

func storageAppFixture(t *testing.T) *App {
	t.Helper()
	root, resolveErr := filepath.EvalSymlinks(t.TempDir())
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(filepath.Join(state, "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	app := &App{ctx: ctx, store: database, dir: state, configDir: root, workers: map[string]*worker{}}
	t.Cleanup(func() { cancel(); app.background.Wait(); database.Close() })
	return app
}
func storageRequest(t *testing.T, a *App, input any) (int, resourceStorageStatus, []byte) {
	t.Helper()
	method := http.MethodGet
	var body []byte
	if input != nil {
		method = http.MethodPost
		body, _ = json.Marshal(input)
	}
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, httptest.NewRequest(method, "/api/resource-storage", bytes.NewReader(body)))
	var result resourceStorageStatus
	if w.Code < 300 {
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	}
	return w.Code, result, w.Body.Bytes()
}
func TestResourceStoragePresetsSeparateChecksSavingAndCutover(t *testing.T) {
	app := storageAppFixture(t)
	s3 := resourcetest.NewS3(t)
	_, current, raw := storageRequest(t, app, nil)
	if !bytes.Contains(raw, []byte(`"activePresetId":""`)) || current.Config.TargetPresetID != nil {
		t.Fatal("local storage must have an empty configuration ID and no pending target")
	}
	initial := current.Config.Revision
	request := func(action, id string, cfg *resourceStorageInput, wantCode int) resourceStorageStatus {
		t.Helper()
		code, result, raw := storageRequest(t, app, resourceStorageRequest{Action: action, PresetID: &id, Revision: current.Config.Revision, Config: cfg})
		if code != wantCode {
			t.Fatalf("%s: got %d, want %d: %s", action, code, wantCode, raw)
		}
		if code == 200 {
			current = result
			for _, secret := range []string{"fixture-access", "fixture-secret", "rotated-secret", "masterKey", "sessionToken"} {
				if bytes.Contains(raw, []byte(secret)) {
					t.Fatal("storage response exposes credentials")
				}
			}
		}
		return result
	}
	input := &resourceStorageInput{Name: "Archive", Endpoint: s3.Server.URL, Bucket: "resources", Region: "us-east-1", AccessKey: "fixture-access", SecretKey: "fixture-secret"}
	request("check", "", input, 200)
	settings, err := resources.LoadSettings(app.configDir)
	if err != nil || len(settings.Backends) != 0 || s3.Count() != 0 || len(current.Config.Presets) != 0 || current.CheckError != "" || current.Config.Migration != nil {
		t.Fatal("a successful unsaved probe published a preset, migration or residue", err)
	}
	request("save", "", input, 200)
	first := current.Config.Presets[0].ID
	other := *input
	other.Name, other.Prefix = "Spare", "spare"
	request("save", "", &other, 200)
	second := current.Config.Presets[1].ID
	if first == second || len(current.Config.Presets) != 2 || current.Config.ActivePresetID != "" || current.Config.Migration != nil || s3.Count() != 0 {
		t.Fatal("preconfiguring destinations changed active storage")
	}
	code, _, _ := storageRequest(t, app, resourceStorageRequest{Action: "migrate", PresetID: &first, Revision: initial})
	if code != 409 {
		t.Fatal("stale selection could start a migration")
	}
	request("save", "", &other, 409)
	request("migrate", "", nil, 200)
	if current.Config.Migration != nil {
		t.Fatal("selecting the active location started a migration")
	}

	s3.SetHook(func(r *http.Request) int {
		if r.Method == http.MethodPut {
			return 403
		}
		return 0
	})
	request("migrate", first, nil, 200)
	if current.CheckError == "" || current.Config.Migration != nil {
		t.Fatal("unverified destination started a migration")
	}
	s3.SetHook(nil)
	if _, _, err := app.store.CleanResources(app.ctx, nil); err != nil {
		t.Fatal(err)
	}
	request("migrate", first, nil, 200)
	if current.Config.Migration == nil || current.Config.Migration.Phase != "syncing" || current.Config.ActivePresetID != "" || current.Config.TargetPresetID == nil || *current.Config.TargetPresetID != first {
		t.Fatal("switch did not publish a durable target with the original still active")
	}
	request("remove", first, nil, 409)
	app.runResourceMigration()
	_, current, _ = storageRequest(t, app, nil)
	if current.Config.ActivePresetID != first || current.Config.Current.Kind != "s3" || current.Config.Migration.Phase != "complete" {
		t.Fatal("cutover did not select the requested preset")
	}
	request("remove", first, nil, 409)
	control := app.store.ResourceControl()
	stamp, _ := control.Current(app.ctx)
	settings, _ = resources.LoadSettings(app.configDir)
	active, _ := settings.Backend(stamp.BackendID)

	input.AccessKey, input.SecretKey = "", ""
	request("check", first, input, 200)
	if current.CheckError != "" {
		t.Fatal("saved credentials could not be checked")
	}
	input.AccessKey, input.SecretKey = "rotated-access", "rotated-secret"
	request("check", first, input, 200)
	settings, _ = resources.LoadSettings(app.configDir)
	checked, _ := settings.Backend(stamp.BackendID)
	if current.CheckError != "" || checked != active {
		t.Fatal("testing replaced live credentials or namespace")
	}
	input.Name = "Renamed"
	request("save", first, input, 200)
	settings, _ = resources.LoadSettings(app.configDir)
	revised, _ := settings.Backend(stamp.BackendID)
	newStamp, _ := control.Current(app.ctx)
	if revised.SecretKey != input.SecretKey || revised.MasterKey != active.MasterKey || current.Config.Presets[0].Name != input.Name || newStamp != stamp {
		t.Fatal("editing an active connection changed its resource identity or lost the edit")
	}
	input.Prefix = "moved"
	request("save", first, input, 409)
	request("migrate", second, nil, 200)
	app.runResourceMigration()
	_, current, _ = storageRequest(t, app, nil)
	if current.Config.ActivePresetID != second {
		t.Fatal("DOFS-to-DOFS cutover failed")
	}
	request("remove", first, nil, 200)
	settings, _ = resources.LoadSettings(app.configDir)
	if _, err := settings.Backend(stamp.BackendID); err != nil || len(current.Config.Presets) != 1 {
		t.Fatal("removing an option lost the old physical backend needed for recovery", err)
	}
	for _, body := range []any{
		resourceStorageRequest{Action: "migrate", Revision: current.Config.Revision},
		map[string]any{"action": "migrate", "revision": current.Config.Revision, "presetId": nil},
	} {
		if code, _, raw := storageRequest(t, app, body); code != http.StatusBadRequest {
			t.Fatalf("omitted or null destination could select local storage: %d %s", code, raw)
		}
	}
	request("migrate", "local", nil, 404) // Local storage is not a named configuration.
	request("migrate", "", nil, 200)
	if current.Config.ActivePresetID != second || current.Config.TargetPresetID == nil || *current.Config.TargetPresetID != "" || current.Config.Migration.Phase != "syncing" {
		t.Fatal("a migration to local must expose an explicit empty target while keeping the original active")
	}
	app.runResourceMigration()
	_, current, _ = storageRequest(t, app, nil)
	if current.Config.ActivePresetID != "" || current.Config.Current.Kind != "local" || current.Config.TargetPresetID != nil || len(current.Config.Presets) != 1 {
		t.Fatal("returning to local storage changed the saved configurations or retained a target")
	}
}

func TestResourceStoragePresetProbeRecoveryAndDormantStatistics(t *testing.T) {
	app := storageAppFixture(t)
	s3 := resourcetest.NewS3(t)
	_, initial, _ := storageRequest(t, app, nil)
	input := &resourceStorageInput{Name: "Offline", Endpoint: "http://127.0.0.1:1", Bucket: "resources", Region: "us-east-1", AccessKey: "fixture-access", SecretKey: "fixture-secret"}
	code, saved, raw := storageRequest(t, app, resourceStorageRequest{Action: "save", Revision: initial.Config.Revision, Config: input})
	if code != 200 {
		t.Fatalf("saving an untested preset: %s", raw)
	}
	if _, err := app.store.ResourceStorage(app.ctx); err != nil {
		t.Fatal("unused preset was queried for inventory", err)
	}
	input.Endpoint = s3.Server.URL
	s3.SetHook(func(r *http.Request) int {
		if r.Method == http.MethodDelete {
			return 403
		}
		return 0
	})
	code, checked, raw := storageRequest(t, app, resourceStorageRequest{Action: "check", Revision: saved.Config.Revision, Config: input})
	if code != 200 || checked.CheckError == "" || len(checked.Config.Presets) != 1 || checked.Config.ActivePresetID != "" || checked.Config.Migration != nil || s3.Count() != 1 {
		t.Fatalf("failed test altered configuration or did not exercise recovery: %d %s (objects: %d)", code, raw, s3.Count())
	}
	s3.SetHook(nil)
	if n, _, err := app.store.CleanResources(app.ctx, nil); err != nil || n != 1 || s3.Count() != 0 {
		t.Fatalf("failed check payload could not be reclaimed: %d %v", n, err)
	}
	for _, input := range []string{`{"action":"migrate","config":{"kind":"s3","masterKey":"not-a-user-setting"}}`, `{"action":"check"}`} {
		w := httptest.NewRecorder()
		app.handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/resource-storage", strings.NewReader(input)))
		if w.Code != 400 {
			t.Fatal("invalid storage input accepted")
		}
	}
}

func TestResourceStoragePreservesLegacyActiveIdentityAndProtectedTarget(t *testing.T) {
	app := storageAppFixture(t)
	s3 := resourcetest.NewS3(t)
	control := app.store.ResourceControl()
	old, err := newStorageBackend(resources.BackendConfig{Kind: "s3", Endpoint: s3.Server.URL, Bucket: "resources", Region: "us-east-1", AccessKey: "fixture-access", SecretKey: "fixture-secret"})
	if err != nil {
		t.Fatal(err)
	}
	old.Unused = false
	other, err := newStorageBackend(old)
	if err != nil {
		t.Fatal(err)
	}
	other.Unused, other.Prefix = false, "other"
	data, _ := json.Marshal(map[string]any{"backends": []resources.BackendConfig{old, other}})
	if err := configstore.Write(app.configDir, configstore.Storage, data); err != nil {
		t.Fatal(err)
	}
	before, _, _ := configstore.Read(app.configDir, configstore.Storage)
	for _, backend := range []resources.BackendConfig{old, other} {
		if err := resources.Prepare(app.ctx, control.Library.StateDir, backend); err != nil {
			t.Fatal(err)
		}
	}
	stamp, _ := control.Current(app.ctx)
	if _, err := control.Start(app.ctx, old.ID, stamp); err != nil {
		t.Fatal(err)
	}
	app.runResourceMigration()
	active, _ := control.Current(app.ctx)
	if _, err := control.Start(app.ctx, other.ID, active); err != nil {
		t.Fatal(err)
	}
	_, view, _ := storageRequest(t, app, nil)
	after, _, _ := configstore.Read(app.configDir, configstore.Storage)
	if !bytes.Equal(before, after) || len(view.Config.Presets) != 2 || view.Config.ActivePresetID != old.ID || view.Config.TargetPresetID == nil || *view.Config.TargetPresetID != other.ID {
		t.Fatal("legacy projection rewrote identity or lost the in-flight selection")
	}
	app.runResourceMigration()
	_, view, _ = storageRequest(t, app, nil)
	if view.Config.ActivePresetID != other.ID {
		t.Fatal("legacy migration could not complete")
	}
	input := &resourceStorageInput{Name: "Saved Archive", Endpoint: other.Endpoint, Bucket: other.Bucket, Region: other.Region, Prefix: other.Prefix}
	code, renamed, raw := storageRequest(t, app, resourceStorageRequest{Action: "save", Revision: view.Config.Revision, PresetID: &other.ID, Config: input})
	if code != 200 || renamed.Config.ActivePresetID != other.ID || renamed.Config.Presets[0].Name != input.Name {
		t.Fatalf("legacy preset could not be edited: %s", raw)
	}
	settings, _ := resources.LoadSettings(app.configDir)
	unchanged, _ := settings.Backend(other.ID)
	if unchanged != other {
		t.Fatal("legacy namespace or keys were replaced")
	}
}
