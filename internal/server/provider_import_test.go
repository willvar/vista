package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"vista/internal/codexauth"
	"vista/internal/modelcatalog"
)

func providerImportSource(t *testing.T, config, auth string) map[string]any {
	t.Helper()
	dir := t.TempDir()
	paths := map[string]any{"source": "opencode", "configPath": filepath.Join(dir, "opencode.json"), "credentialPath": filepath.Join(dir, "opencode.db")}
	if err := os.WriteFile(paths["configPath"].(string), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	providerImportCredentials(t, paths["credentialPath"].(string), auth)
	return paths
}

func providerImportCredentials(t *testing.T, path, contents string) {
	t.Helper()
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(contents), &values); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, query := range []string{`CREATE TABLE IF NOT EXISTS credential (id TEXT PRIMARY KEY, integration_id TEXT, value TEXT NOT NULL, active INTEGER, time_created INTEGER NOT NULL)`, `DELETE FROM credential`} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for id, value := range values {
		if _, err := db.Exec(`INSERT INTO credential VALUES (?, ?, ?, 1, 1)`, id, id, string(value)); err != nil {
			t.Fatal(err)
		}
	}
}

func providerImportRequest(t *testing.T, app *App, input map[string]any, action string) *httptest.ResponseRecorder {
	t.Helper()
	input["action"] = action
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	app.importProviders(w, httptest.NewRequest(http.MethodPost, "/api/provider-import", bytes.NewReader(body)))
	return w
}

func providerImportPreview(t *testing.T, app *App, input map[string]any) modelcatalog.OpenCodeImport {
	t.Helper()
	w := providerImportRequest(t, app, input, "preview")
	var preview modelcatalog.OpenCodeImport
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &preview) != nil {
		t.Fatalf("preview: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatal("credential was exposed in preview")
	}
	input["fingerprint"] = preview.Fingerprint
	return preview
}

func TestProviderImportPreviewDoesNotWriteOrContactProvider(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-secret" || r.Header.Get("X-Import") != "header-secret" {
			t.Error("discovery did not use imported credentials")
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"discovered"}]}`))
	}))
	defer upstream.Close()
	app := directoryTestApp(t)
	input := providerImportSource(t, `{"providers":{"gateway":{"name":"Imported gateway","package":"@opencode/ai/providers/openai-compatible","settings":{"baseURL":"`+upstream.URL+`/v1","apiKey":"test-secret"},"headers":{"X-Import":"header-secret"}}}}`, `{}`)
	preview := providerImportPreview(t, app, input)
	if len(preview.Connections) != 1 || preview.Connections[0].Reason != "" || requests.Load() != 0 {
		t.Fatal("preview made an upstream request or failed to read connection")
	}
	if _, err := os.Stat(filepath.Join(app.configDir, "models.json")); !os.IsNotExist(err) {
		t.Fatal("preview wrote model configuration")
	}
	input["ids"] = []string{"gateway"}
	w := providerImportRequest(t, app, input, "import")
	if w.Code != 200 || requests.Load() != 1 {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
	raw, exists, err := modelcatalog.Read(app.configDir)
	if err != nil || !exists || len(raw.Models) != 1 || raw.Models[0].ID != "discovered" || raw.Models[0].Headers["X-Import"] != "header-secret" || raw.Models[0].ConnectionID == "" {
		t.Fatal("discovered connection was not saved")
	}
}

func TestProviderImportConflictingCredentialSourcesRequireOneSelection(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer legacy-secret" {
			t.Error("import did not use the selected credential source")
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"discovered"}]}`))
	}))
	defer upstream.Close()
	app := directoryTestApp(t)
	input := providerImportSource(t, `{"provider":{"gateway":{"npm":"@ai-sdk/openai-compatible","options":{"baseURL":"`+upstream.URL+`/v1"}}}}`, `{}`)
	dataRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataRoot)
	dataDir := filepath.Join(dataRoot, "opencode")
	if err := os.Mkdir(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(dataDir, "auth.json")
	authData := `{"gateway":{"type":"api","key":"legacy-secret"}}`
	if err := os.WriteFile(authPath, []byte(authData), 0600); err != nil {
		t.Fatal(err)
	}
	providerImportCredentials(t, filepath.Join(dataDir, "opencode.db"), `{"gateway":{"type":"key","key":"new-secret"}}`)
	input["credentialPath"] = ""
	preview := providerImportPreview(t, app, input)
	if len(preview.Connections) != 2 || preview.Connections[0].CredentialGroup != "gateway" || preview.Connections[1].CredentialGroup != "gateway" {
		t.Fatal("preview did not expose the credential conflict")
	}
	input["ids"] = []string{preview.Connections[0].ID, preview.Connections[1].ID}
	if w := providerImportRequest(t, app, input, "import"); w.Code != 400 || requests.Load() != 0 {
		t.Fatal("conflicting selection was not rejected before contacting the provider")
	}
	if err := os.WriteFile(authPath, []byte(`{"gateway":{"type":"api","key":"changed-secret"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	input["ids"] = []string{preview.Connections[0].ID}
	if w := providerImportRequest(t, app, input, "import"); w.Code != 409 || requests.Load() != 0 {
		t.Fatal("changed v1 credentials were imported from a stale preview")
	}
	if err := os.WriteFile(authPath, []byte(authData), 0600); err != nil {
		t.Fatal(err)
	}
	if w := providerImportRequest(t, app, input, "import"); w.Code != 200 || requests.Load() != 1 {
		t.Fatalf("single-source import failed: %d %s", w.Code, w.Body)
	}
	stored, _, err := modelcatalog.Read(app.configDir)
	if err != nil || len(stored.Models) != 1 || stored.Models[0].APIKey != "legacy-secret" {
		t.Fatal("selected source did not survive saving", err)
	}
	if data, err := os.ReadFile(authPath); err != nil || string(data) != authData {
		t.Fatal("import modified OpenCode credentials")
	}
}

func TestProviderImportKeepsExistingConnectionsAndDefault(t *testing.T) {
	app := directoryTestApp(t)
	if err := app.changeProviderSettings(context.Background(), http.MethodPost, providerSettingsInput{Name: "Existing", Provider: "catalog", ProviderID: "deepseek", CredentialMode: "apiKey", APIKey: "original-secret"}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := modelcatalog.Read(app.configDir)
	input := providerImportSource(t, `{}`, `{"deepseek":{"type":"key","key":"other-secret"},"openai":{"type":"key","key":"new-secret"}}`)
	preview := providerImportPreview(t, app, input)
	if preview.Connections[0].Reason != "existing" || preview.Connections[1].Reason != "" {
		t.Fatal("existing destination not excluded")
	}
	input["ids"] = []string{"deepseek", "openai"}
	if w := providerImportRequest(t, app, input, "import"); w.Code != 409 {
		t.Fatalf("existing connection accepted: %d", w.Code)
	}
	input["ids"] = []string{"openai"}
	if w := providerImportRequest(t, app, input, "import"); w.Code != 200 {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
	after, _, _ := modelcatalog.Read(app.configDir)
	if after.Default != before.Default || len(after.Models) != 2 || after.Models[0].ConnectionID != before.Models[0].ConnectionID || after.Models[0].APIKey != "original-secret" {
		t.Fatal("import replaced an existing connection or default")
	}
}

func TestProviderImportRejectsChangedSourceAndInvalidSelection(t *testing.T) {
	app := directoryTestApp(t)
	input := providerImportSource(t, `{}`, `{"deepseek":{"type":"key","key":"test-secret"}}`)
	providerImportPreview(t, app, input)
	for _, ids := range [][]string{nil, {"unknown"}, {"deepseek", "deepseek"}} {
		input["ids"] = ids
		if w := providerImportRequest(t, app, input, "import"); w.Code != 400 {
			t.Fatalf("invalid selection accepted: %v %d", ids, w.Code)
		}
	}
	input["ids"] = []string{"deepseek"}
	providerImportCredentials(t, input["credentialPath"].(string), `{"deepseek":{"type":"key","key":"changed-secret"}}`)
	w := providerImportRequest(t, app, input, "import")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "settings_conflict") {
		t.Fatalf("stale import accepted: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(app.configDir, "models.json")); !os.IsNotExist(err) {
		t.Fatal("rejected import changed model configuration")
	}
}

func TestProviderImportIsAtomicWhenDiscoveryFails(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer upstream.Close()
	app := directoryTestApp(t)
	input := providerImportSource(t, `{"providers":{"zzz":{"package":"@opencode/ai/providers/openai-compatible","settings":{"baseURL":"`+upstream.URL+`/v1","apiKey":"key"}}}}`, `{"deepseek":{"type":"key","key":"test-secret"}}`)
	providerImportPreview(t, app, input)
	input["ids"] = []string{"deepseek", "zzz"}
	if w := providerImportRequest(t, app, input, "import"); w.Code != 400 {
		t.Fatalf("failed discovery accepted: %d %s", w.Code, w.Body)
	}
	if _, exists, err := modelcatalog.Read(app.configDir); err != nil || exists || app.modelRevision != 0 {
		t.Fatal("part of a failed batch was saved")
	}
}

func TestProviderImportReportsFailedConnectionAndRedactedUpstreamReason(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"api_key_expired","message":"Create a new API key. Rejected: expired-secret / header-secret"}}`))
	}))
	defer upstream.Close()
	app := directoryTestApp(t)
	input := providerImportSource(t, `{"providers":{"zzz":{"name":"Expired gateway","package":"@opencode/ai/providers/openai-compatible","settings":{"baseURL":"`+upstream.URL+`/v1","apiKey":"expired-secret"},"headers":{"X-Key":"header-secret"}}}}`, `{"deepseek":{"type":"key","key":"valid-secret"}}`)
	providerImportPreview(t, app, input)
	input["ids"] = []string{"deepseek", "zzz"}
	w := providerImportRequest(t, app, input, "import")
	var detail modelcatalog.SettingsError
	if w.Code != http.StatusBadRequest || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail.Code != "model_ids_required" {
		t.Fatalf("unexpected error: %d %s", w.Code, w.Body)
	}
	for _, want := range []string{"Expired gateway", "HTTP 401", "api_key_expired", "Create a new API key"} {
		if !strings.Contains(detail.Message, want) {
			t.Errorf("missing %q in error: %s", want, detail.Message)
		}
	}
	for _, secret := range []string{"expired-secret", "header-secret", "valid-secret"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("credential leaked in import error")
		}
	}
	if _, exists, _ := modelcatalog.Read(app.configDir); exists {
		t.Fatal("failed import saved a partial batch")
	}
	input["ids"] = []string{"deepseek"}
	if retry := providerImportRequest(t, app, input, "import"); retry.Code != http.StatusOK {
		t.Fatalf("deselecting the failed connection did not recover: %d %s", retry.Code, retry.Body)
	}
}

func TestProviderImportSavesCodexCredentialsAndRollsBackOnFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "save failure"}[fail], func(t *testing.T) {
			app := directoryTestApp(t)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if fail {
					if err := os.Mkdir(filepath.Join(app.configDir, "models.json"), 0700); err != nil {
						t.Error(err)
					}
				}
				_, _ = w.Write([]byte(`{"data":[{"id":"imported"}]}`))
			}))
			defer upstream.Close()
			input := providerImportSource(t, `{"providers":{"zzz":{"package":"@opencode/ai/providers/openai-compatible","settings":{"baseURL":"`+upstream.URL+`/v1"}}}}`, `{"openai":{"type":"oauth","access":"access-secret","refresh":"refresh-secret","methodID":"chatgpt-browser","metadata":{"accountID":"account"},"expires":1900000000000}}`)
			providerImportPreview(t, app, input)
			input["ids"] = []string{"openai", "zzz"}
			w := providerImportRequest(t, app, input, "import")
			files, _ := filepath.Glob(filepath.Join(app.configDir, "codex-auth", "*.json"))
			if fail {
				if w.Code != 400 || len(files) != 0 || app.modelRevision != 0 {
					t.Fatalf("failed transaction left credentials: %d, %d files", w.Code, len(files))
				}
				return
			}
			if w.Code != 200 || len(files) != 1 {
				t.Fatalf("Codex import: %d %s", w.Code, w.Body)
			}
			credential, err := codexauth.Read(context.Background(), files[0])
			if err != nil || credential.RefreshToken != "refresh-secret" || credential.AccountID != "account" {
				t.Fatal("Codex credential was not saved intact")
			}
		})
	}
}
