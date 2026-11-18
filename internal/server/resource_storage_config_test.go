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
func TestResourceStorageConfigurationChecksDoNotSwitchAndRemainPrivate(t *testing.T) {
	app := storageAppFixture(t)
	s3 := resourcetest.NewS3(t)
	_, initial, _ := storageRequest(t, app, nil)
	config := &resourceStorageInput{Kind: "s3", Endpoint: s3.Server.URL, Bucket: "resources", Region: "us-east-1", AccessKey: "fixture-access", SecretKey: "fixture-secret"}
	code, checked, raw := storageRequest(t, app, resourceStorageRequest{Action: "check", Revision: initial.Config.Revision, Config: config})
	if code != 200 || checked.CheckError != "" || checked.Config == nil || checked.Config.Current.Kind != "local" {
		t.Fatalf("storage check switched or failed: %d %s", code, raw)
	}
	if bytes.Contains(raw, []byte(config.AccessKey)) || bytes.Contains(raw, []byte(config.SecretKey)) || bytes.Contains(raw, []byte("masterKey")) {
		t.Fatal("storage status leaked credentials")
	}
	if s3.Count() != 0 {
		t.Fatal("successful storage check left its remote payload")
	}
	code, _, _ = storageRequest(t, app, resourceStorageRequest{Action: "migrate", Revision: initial.Config.Revision, Config: config})
	if code != 409 {
		t.Fatal("stale storage editor was accepted")
	}
	config.AccessKey, config.SecretKey = "", ""
	code, started, _ := storageRequest(t, app, resourceStorageRequest{Action: "migrate", Revision: checked.Config.Revision, Config: config})
	if code != 200 || started.Config.Migration == nil || started.Config.Migration.Phase != "syncing" {
		t.Fatal("storage migration not durably started")
	}
	// With no originals, cutover must still be a real task/state transaction.
	app.runResourceMigration()
	_, complete, _ := storageRequest(t, app, nil)
	if complete.Config.Current.Kind != "s3" || complete.Config.Migration.Phase != "complete" {
		t.Fatal("storage task did not commit cutover")
	}
	if s3.Count() != 0 {
		t.Fatal("empty migration created an object")
	}
	code, blankCheck, _ := storageRequest(t, app, resourceStorageRequest{Action: "check", Revision: complete.Config.Revision, Config: config})
	if code != 200 || blankCheck.CheckError != "" {
		t.Fatal("saved active credentials could not be checked without re-entry")
	}
	complete = blankCheck
	// Checking replacement credentials must not change the in-use credentials.
	config.AccessKey, config.SecretKey = "replacement-access", "replacement-secret"
	code, rechecked, _ := storageRequest(t, app, resourceStorageRequest{Action: "check", Revision: complete.Config.Revision, Config: config})
	if code != 200 || rechecked.CheckError != "" {
		t.Fatal("credential check failed")
	}
	settings, err := resources.LoadSettings(app.configDir)
	if err != nil {
		t.Fatal(err)
	}
	stamp, _ := app.store.ResourceControl().Current(app.ctx)
	active, _ := settings.Backend(stamp.BackendID)
	if active.AccessKey != "fixture-access" {
		t.Fatal("checking credentials changed the active backend")
	}
	config.AccessKey, config.SecretKey = "", ""
	code, applied, _ := storageRequest(t, app, resourceStorageRequest{Action: "migrate", Revision: rechecked.Config.Revision, Config: config})
	if code != 200 || applied.CheckError != "" {
		t.Fatal("credential application failed")
	}
	settings, _ = resources.LoadSettings(app.configDir)
	active, _ = settings.Backend(stamp.BackendID)
	if active.AccessKey != "replacement-access" {
		t.Fatal("explicit application did not update credentials")
	}
	current, _ := app.store.ResourceControl().Current(app.ctx)
	if current != stamp {
		t.Fatal("credential update incorrectly migrated an unchanged location")
	}
}
func TestResourceStorageFailedCheckRetainsDraftRevisionAndCanBeCleaned(t *testing.T) {
	app := storageAppFixture(t)
	s3 := resourcetest.NewS3(t)
	s3.SetHook(func(r *http.Request) int {
		if r.Method == http.MethodPut {
			return 403
		}
		return 0
	})
	_, initial, _ := storageRequest(t, app, nil)
	config := &resourceStorageInput{Kind: "s3", Endpoint: s3.Server.URL, Bucket: "resources", Region: "us-east-1", AccessKey: "fixture-access", SecretKey: "fixture-secret"}
	code, checked, raw := storageRequest(t, app, resourceStorageRequest{Action: "check", Revision: initial.Config.Revision, Config: config})
	if code != 200 || checked.CheckError == "" || checked.Config.Current.Kind != "local" || checked.Config.Revision == initial.Config.Revision {
		t.Fatalf("failed check state: %d %s", code, raw)
	}
	s3.SetHook(nil)
	if _, _, err := app.store.CleanResources(app.ctx, nil); err != nil {
		t.Fatal("failed check recovery record could not be reclaimed", err)
	}
	for _, input := range []string{`{"action":"migrate","config":{"kind":"s3","masterKey":"not-a-user-setting"}}`, `{"action":"check"}`} {
		w := httptest.NewRecorder()
		app.handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/resource-storage", strings.NewReader(input)))
		if w.Code != 400 {
			t.Fatal("invalid storage input accepted")
		}
	}
}
