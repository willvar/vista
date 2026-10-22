package modelcatalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vista/internal/opencodeconfig"
)

func openCodeImportFiles(t *testing.T) (config, auth string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	config = filepath.Join(root, "config", "opencode", "opencode.json")
	auth = filepath.Join(root, "data", "opencode", "opencode.db")
	for _, path := range []string{config, auth} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return config, auth
}

func writeOpenCodeFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCodeImportCredentialsWithoutProviderConfig(t *testing.T) {
	_, auth := openCodeImportFiles(t)
	writeOpenCodeAuth(t, auth, `{"deepseek":{"type":"key","key":"api-secret"},"openai":{"type":"oauth","access":"access-secret","refresh":"refresh-secret","methodID":"chatgpt-headless","metadata":{"accountID":"account"},"expires":1900000000000},"anthropic":{"type":"oauth","access":"unsupported-secret"}}`)
	preview, err := ReadOpenCodeImport(context.Background(), "", "")
	if err != nil || len(preview.Connections) != 3 || len(preview.ConfigPaths) != 0 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	deepseek, codex := preview.Connections[1], preview.Connections[2]
	if deepseek.Input.ProviderID != "deepseek" || deepseek.Input.APIKey != "api-secret" || deepseek.Reason != "" {
		t.Fatalf("API connection: %+v", deepseek)
	}
	if codex.Input.ProviderID != "openai-codex" || codex.Credential == nil || codex.Credential.RefreshToken != "refresh-secret" || codex.Reason != "" {
		t.Fatalf("Codex account conversion failed: %s", codex.Reason)
	}
	if preview.Connections[0].Reason != "oauth" {
		t.Fatal("unsupported OAuth was accepted")
	}
	data, err := json.Marshal(preview)
	if err != nil || strings.Contains(string(data), "secret") || strings.Contains(string(data), "accountID") || strings.Contains(string(data), "Input") {
		t.Fatal("preview exposed credentials")
	}
	before := preview.Fingerprint
	writeOpenCodeAuth(t, auth, `{"deepseek":{"type":"key","key":"changed-secret"}}`)
	changed, err := ReadOpenCodeImport(context.Background(), "", "")
	if err != nil || changed.Fingerprint == before {
		t.Fatal("credential change did not invalidate preview")
	}
}

func TestOpenCodeImportCustomFormatsAndReferences(t *testing.T) {
	config, _ := openCodeImportFiles(t)
	t.Setenv("VISTA_IMPORT_TEST_KEY", "env-secret")
	writeOpenCodeFixture(t, filepath.Join(filepath.Dir(config), "header.txt"), "header-secret\n")
	writeOpenCodeFixture(t, config, `{
      // URL comments and trailing commas must preserve strings.
      "providers": {
        "chat": {"package":"@opencode/ai/providers/openai-compatible","name":"Chat gateway","settings":{"baseURL":"https://gateway.example/v1","apiKey":"{env:VISTA_IMPORT_TEST_KEY}"},"headers":{"x-test":"{file:header.txt}"},"models":{"alias":{"modelID":"actual-model"},},},
        "responses": {"package":"@opencode/ai/providers/openai/responses","settings":{"baseURL":"https://gateway.example/v1","apiKey":"key"}},
        "claude": {"package":"@opencode/ai/providers/anthropic","settings":{"baseURL":"https://gateway.example/v1","apiKey":"key"}},
        "gemini": {"package":"@opencode/ai/providers/google","settings":{"baseURL":"https://gateway.example/v1","apiKey":"key"}},
        "cohere": {"package":"@opencode/ai/providers/cohere","settings":{"baseURL":"https://gateway.example/v2","apiKey":"key"}},
        "anonymous": {"package":"@opencode/ai/providers/openai-compatible","settings":{"baseURL":"http://127.0.0.1:8000/v1"}},
        "header-only": {"package":"@opencode/ai/providers/openai-compatible","settings":{"baseURL":"https://gateway.example/v1"},"headers":{"Authorization":"Bearer {env:VISTA_IMPORT_TEST_KEY}"}},
      },
    }`)
	preview, err := ReadOpenCodeImport(context.Background(), "", "")
	if err != nil || len(preview.Connections) != 7 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	formats := map[string]string{"chat": "openai-chat", "responses": "openai-responses", "claude": "anthropic", "gemini": "gemini", "cohere": "cohere", "anonymous": "openai-chat", "header-only": "openai-chat"}
	for _, entry := range preview.Connections {
		if entry.Reason != "" || entry.Input.Provider != formats[entry.ID] || entry.ProviderID != "custom" {
			t.Fatalf("%s conversion: %s, %s", entry.ID, entry.Reason, entry.Input.Provider)
		}
		if entry.ID == "chat" && (entry.Input.Headers["X-Test"] != "header-secret" || entry.Input.APIKey != "env-secret" || len(entry.Input.IDs) != 1 || entry.Input.IDs[0] != "actual-model") {
			t.Fatal("lost model ID, key or file reference")
		}
		if (entry.ID == "anonymous" || entry.ID == "header-only") && entry.Input.CredentialMode != "none" {
			t.Fatal("unauthenticated or header-only connection requires a key")
		}
	}
	data, _ := json.Marshal(preview)
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "Authorization") {
		t.Fatal("preview exposed a secret header")
	}
}

func TestOpenCodeImportSkipsNonEquivalentSettings(t *testing.T) {
	config, auth := openCodeImportFiles(t)
	writeOpenCodeAuth(t, auth, `{"anthropic":{"type":"key","key":"test"},"deepseek":{"type":"key","key":"test"}}`)
	writeOpenCodeFixture(t, config, `{"providers":{
      "anthropic":{"models":{"alias":{"package":"some-plugin"}}},
      "unknown":{"package":"some-plugin"},
      "unresolved":{"package":"@opencode/ai/providers/openai-compatible","settings":{"apiKey":"{file:missing}"}},
      "missing":{"package":"@opencode/ai/providers/openai-compatible","settings":{"apiKey":"{env:VISTA_MISSING_IMPORT_TEST}"}},
      "ollama":{},"openai":{}
    }}`)
	t.Setenv("VISTA_MISSING_IMPORT_TEST", "")
	preview, err := ReadOpenCodeImport(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"anthropic": "unsupported", "deepseek": "", "unknown": "unsupported", "unresolved": "reference", "missing": "invalid", "ollama": "", "openai": "credentials"}
	for _, entry := range preview.Connections {
		if entry.Reason != want[entry.ID] {
			t.Fatalf("%s: %s, want %s", entry.ID, entry.Reason, want[entry.ID])
		}
		if entry.ID == "ollama" && (entry.Input.CredentialMode != "default" || entry.Input.BaseURL != "http://127.0.0.1:11434/v1") {
			t.Fatal("local default authentication was lost")
		}
	}
}

func TestOpenCodeImportLayersAndExistingConnections(t *testing.T) {
	config, auth := openCodeImportFiles(t)
	writeOpenCodeAuth(t, auth, `{"deepseek":{"type":"key","key":"test"}}`)
	writeOpenCodeFixture(t, config, `{"providers":{"gateway":{"name":"Gateway","package":"@opencode/ai/providers/openai-compatible","settings":{"baseURL":"https://gateway.example/v1","apiKey":"test"}}}}`)
	writeOpenCodeFixture(t, config+"c", `{"providers":{"gateway":{"headers":{"x-extra":"value"}}}}`)
	preview, err := ReadOpenCodeImport(context.Background(), "", "")
	if err != nil || len(preview.ConfigPaths) != 2 || len(preview.Connections) != 2 || preview.Connections[1].Input.Headers["X-Extra"] != "value" {
		t.Fatalf("layered settings: %+v %v", preview, err)
	}
	preview.MarkExisting(Catalog{Models: []Configured{
		{Provider: "catalog", ProviderID: "deepseek"},
		{Provider: "openai-chat", ProviderID: "custom", BaseURL: "https://gateway.example/v1/"},
	}})
	for _, entry := range preview.Connections {
		if entry.Reason != "existing" {
			t.Fatal("existing connection was not skipped")
		}
	}
	// A later fragment must not erase an unresolved earlier transport field.
	writeOpenCodeFixture(t, config, `{"providers":{"gateway":{"settings":{"baseURL":"{file:missing}"}}}}`)
	preview, err = ReadOpenCodeImport(context.Background(), "", "")
	if err != nil || preview.Connections[1].Reason != "reference" {
		t.Fatal("unresolved configuration was silently dropped")
	}
}

func TestOpenCodeV2UsesSelectedCredentialFromReadOnlyDatabase(t *testing.T) {
	_, path := openCodeImportFiles(t)
	writeOpenCodeAuth(t, path, `{"deepseek":{"type":"key","key":"active-secret"}}`)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Keep WAL live: a read must include committed WAL rows without copying or
	// migrating OpenCode's database, and prefer the active over a newer key.
	for _, query := range []string{
		`PRAGMA journal_mode=WAL`,
		`INSERT INTO credential VALUES ('newer', 'deepseek', '{"type":"key","key":"inactive-secret"}', 0, 100)`,
		`INSERT INTO credential VALUES ('older', 'openai', '{"type":"key","key":"old-secret"}', NULL, 1)`,
		`INSERT INTO credential VALUES ('latest', 'openai', '{"type":"key","key":"latest-secret"}', NULL, 2)`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := ReadOpenCodeImport(context.Background(), "", "")
	if err != nil || len(preview.Connections) != 2 {
		t.Fatalf("v2 database read: %v", err)
	}
	if preview.Connections[0].Input.APIKey != "active-secret" || preview.Connections[1].Input.APIKey != "latest-secret" {
		t.Fatal("import selected an inactive or older credential")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("preview changed the source database")
	}
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM credential`).Scan(&rows); err != nil || rows != 4 {
		t.Fatal("preview changed credential rows")
	}
}

func TestOpenCodeV2CanonicalProviderAndDynamicPlugins(t *testing.T) {
	config, _ := openCodeImportFiles(t)
	t.Setenv("VISTA_TEST_OPEN_CODE_KEY", "canonical-secret")
	writeOpenCodeFixture(t, config, `{"providers":{
	  "alias":{"canonical":"deepseek","name":"DeepSeek alias","env":["VISTA_TEST_OPEN_CODE_KEY"]},
	  "gateway":{"package":"aisdk:@ai-sdk/openai","settings":{"baseURL":"https://gateway.example/v1","apiKey":"key"},"models":{"alias":{"modelID":"actual-model"},"off":{"disabled":true}}}
	},"plugins":[{"package":"./extensions/gateway","options":{"apiKey":"plugin-secret"}},{"package":"https://user:password@example.test/plugin?token=secret#secret"}]}`)
	preview, err := ReadOpenCodeImport(context.Background(), "", "")
	if err != nil || len(preview.Connections) != 2 || len(preview.Plugins) != 2 {
		t.Fatalf("v2 config: %v", err)
	}
	alias, gateway := preview.Connections[0], preview.Connections[1]
	if alias.Reason != "" || alias.Input.ProviderID != "deepseek" || alias.Input.APIKey != "canonical-secret" || alias.Name != "DeepSeek alias" {
		t.Fatal("canonical provider or its configured environment was lost")
	}
	if gateway.Reason != "" || gateway.Input.Provider != "openai-responses" || len(gateway.Input.IDs) != 1 || gateway.Input.IDs[0] != "actual-model" {
		t.Fatal("v2 SDK package or model aliases were lost")
	}
	encoded, _ := json.Marshal(preview)
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "password") {
		t.Fatal("plugin options or URL credentials leaked into preview")
	}
}

func TestOpenCodeImportReadsLegacyConfigAndCredentials(t *testing.T) {
	config, path := openCodeImportFiles(t)
	legacy := filepath.Join(filepath.Dir(path), "auth.json")
	credentials := `{"deepseek":{"type":"api","key":"legacy-secret"},"openai":{"type":"oauth","access":"access-secret","refresh":"refresh-secret","expires":1900000000000,"accountId":"account"},"github-copilot":{"type":"oauth","refresh":"copilot-secret","access":"copilot-secret","expires":0,"enterpriseUrl":"https://github.example"}}`
	writeOpenCodeFixture(t, legacy, credentials)
	t.Setenv("VISTA_IMPORT_TEST_KEY", "gateway-secret")
	writeOpenCodeFixture(t, config, `{"provider":{"gateway":{"npm":"@ai-sdk/openai-compatible","name":"V1 gateway","options":{"apiKey":"{env:VISTA_IMPORT_TEST_KEY}","baseURL":"https://old.invalid/v1","headers":{"X-Test":"header-secret"}},"api":"https://gateway.example/v1","models":{"alias":{"id":"actual-model"}}}},"plugin":["local-plugin",["https://user:secret@example.test/plugin?token=secret",{}]]}`)
	preview, err := ReadOpenCodeImport(context.Background(), "", "")
	if err != nil || len(preview.Connections) != 4 || len(preview.CredentialPaths) != 1 || preview.CredentialPaths[0] != legacy || len(preview.Plugins) != 2 {
		t.Fatalf("legacy import: %v", err)
	}
	for _, entry := range preview.Connections {
		if entry.Reason != "" {
			t.Fatal("legacy connection rejected", entry.ID, entry.Reason)
		}
		switch entry.ID {
		case "deepseek":
			if entry.Input.APIKey != "legacy-secret" || entry.Input.ProviderID != "deepseek" {
				t.Fatal("legacy API credential lost")
			}
		case "gateway":
			if entry.Input.Provider != "openai-chat" || entry.BaseURL != "https://gateway.example/v1" || entry.Input.APIKey != "gateway-secret" || entry.Input.Headers["X-Test"] != "header-secret" || len(entry.Input.IDs) != 1 || entry.Input.IDs[0] != "actual-model" {
				t.Fatal("legacy provider fields lost")
			}
		case "openai":
			if entry.Credential == nil || entry.Credential.AccountID != "account" || entry.Credential.RefreshToken != "refresh-secret" {
				t.Fatal("legacy Codex account lost")
			}
		case "github-copilot":
			if entry.Input.CopilotAuth == nil || entry.Input.CopilotAuth.EnterpriseURL != "https://github.example" || entry.Input.APIKey != "copilot-secret" {
				t.Fatal("legacy Copilot account lost")
			}
		}
	}
	data, _ := json.Marshal(preview)
	if strings.Contains(string(data), "secret") {
		t.Fatal("legacy secrets exposed")
	}
	before, _ := os.ReadFile(legacy)
	if string(before) != credentials {
		t.Fatal("legacy credentials modified")
	}
}

func TestOpenCodeImportDeduplicatesCredentialsAndExposesConflictingChoices(t *testing.T) {
	config, path := openCodeImportFiles(t)
	legacy := filepath.Join(filepath.Dir(path), "auth.json")
	writeOpenCodeFixture(t, legacy, `{"deepseek":{"type":"api","key":"same-secret"},"openai":{"type":"api","key":"legacy-secret"}}`)
	writeOpenCodeAuth(t, path, `{"deepseek":{"type":"key","key":"same-secret"},"openai":{"type":"key","key":"new-secret"}}`)
	preview, err := ReadOpenCodeImport(context.Background(), "", "")
	if err != nil || len(preview.Connections) != 3 || len(preview.CredentialPaths) != 2 {
		t.Fatal("combined import failed", err)
	}
	if preview.Connections[0].ID != "deepseek" || preview.Connections[0].CredentialGroup != "" || preview.Connections[0].Input.APIKey != "same-secret" {
		t.Fatal("equivalent credentials not deduplicated")
	}
	choices := preview.Connections[1:]
	if choices[0].ID == choices[1].ID || choices[0].CredentialGroup != "openai" || choices[1].CredentialGroup != "openai" || choices[0].CredentialSource != "auth.json" || choices[1].CredentialSource != "opencode.db" {
		t.Fatal("credential conflict missing distinct, selectable sources")
	}
	if choices[0].Input.APIKey != "legacy-secret" || choices[1].Input.APIKey != "new-secret" {
		t.Fatal("conflict choices do not retain their own credentials")
	}
	// A configured API key overrides either login store, so no choice is needed.
	writeOpenCodeFixture(t, config, `{"providers":{"openai":{"settings":{"apiKey":"inline-secret"}}}}`)
	inline, err := ReadOpenCodeImport(context.Background(), "", "")
	if err != nil || len(inline.Connections) != 2 || inline.Connections[1].CredentialGroup != "" || inline.Connections[1].Input.APIKey != "inline-secret" {
		t.Fatal("unused stored credentials created a spurious conflict", err)
	}
	writeOpenCodeFixture(t, legacy, `{"deepseek":{"type":"api","key":"same-secret"},"openai":{"type":"api","key":"changed-secret"}}`)
	changed, err := ReadOpenCodeImport(context.Background(), "", "")
	if err != nil || changed.Fingerprint == inline.Fingerprint {
		t.Fatal("legacy source changes failed to invalidate the preview")
	}
}

func TestOpenCodeImportMixedFormatsUseNativeEntryBeforeMergingLayers(t *testing.T) {
	config, _ := openCodeImportFiles(t)
	writeOpenCodeFixture(t, config, `{"provider":{"gateway":{"npm":"unknown-plugin","options":{"apiKey":"{file:missing}","headers":{"X-Old":"old-secret"}}}},"providers":{"gateway":{"package":"@opencode/ai/providers/openai-compatible","settings":{"baseURL":"https://gateway.example/v1","apiKey":"new-secret"}}}}`)
	writeOpenCodeFixture(t, config+"c", `{"provider":{"gateway":{"options":{"headers":{"X-New":"new-header-secret"}}}}}`)
	preview, err := ReadOpenCodeImport(context.Background(), "", "")
	if err != nil || len(preview.Connections) != 1 || preview.Connections[0].Reason != "" {
		t.Fatal("native replacement or mixed file layers failed", err)
	}
	entry := preview.Connections[0]
	if entry.Input.APIKey != "new-secret" || entry.Input.Provider != "openai-chat" || len(entry.Input.Headers) != 1 || entry.Input.Headers["X-New"] != "new-header-secret" {
		t.Fatal("obsolete legacy fields leaked into the effective provider")
	}
}

func TestOpenCodeImportDoesNotDiscardLegacyTransportOverrides(t *testing.T) {
	for name, extra := range map[string]string{
		"request body":       `"options":{"baseURL":"https://gateway.example/v1","apiKey":"test-secret","body":{"temperature":0.5}}`,
		"timeout":            `"options":{"baseURL":"https://gateway.example/v1","apiKey":"test-secret","timeout":1000}`,
		"model settings":     `"options":{"baseURL":"https://gateway.example/v1","apiKey":"test-secret"},"models":{"model":{"options":{"temperature":0.5}}}`,
		"model package":      `"options":{"baseURL":"https://gateway.example/v1","apiKey":"test-secret"},"models":{"model":{"provider":{"npm":"@ai-sdk/anthropic"}}}`,
		"model endpoint":     `"options":{"baseURL":"https://gateway.example/v1","apiKey":"test-secret"},"models":{"model":{"provider":{"api":"https://other.example/v1"}}}`,
		"model allowlist":    `"options":{"baseURL":"https://gateway.example/v1","apiKey":"test-secret"},"whitelist":["only-this-model"]`,
		"cache key behavior": `"options":{"baseURL":"https://gateway.example/v1","apiKey":"test-secret","setCacheKey":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			config, _ := openCodeImportFiles(t)
			writeOpenCodeFixture(t, config, `{"provider":{"gateway":{"npm":"@ai-sdk/openai-compatible",`+extra+`}}}`)
			preview, err := ReadOpenCodeImport(context.Background(), "", "")
			if err != nil || len(preview.Connections) != 1 || preview.Connections[0].Reason != "unsupported" {
				t.Fatal("non-equivalent v1 behavior was silently discarded", err)
			}
		})
	}
}

func TestOpenCodeImportAcceptsExplicitJSONCredentialsAndIgnoresV1SessionDatabase(t *testing.T) {
	_, path := openCodeImportFiles(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE session (id TEXT)`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(filepath.Dir(path), "auth.json")
	writeOpenCodeFixture(t, legacy, `{"deepseek":{"type":"api","key":"test-secret"}}`)
	preview, err := ReadOpenCodeImport(context.Background(), "", "")
	if err != nil || len(preview.Connections) != 1 || len(preview.CredentialPaths) != 1 {
		t.Fatal("v1 session database blocked auth.json import", err)
	}
	custom := filepath.Join(filepath.Dir(path), "credentials.store")
	writeOpenCodeFixture(t, custom, `{"openai":{"type":"api","key":"explicit-secret"}}`)
	preview, err = ReadOpenCodeImport(context.Background(), "", custom)
	if err != nil || len(preview.Connections) != 1 || preview.Connections[0].Input.APIKey != "explicit-secret" || len(preview.CredentialPaths) != 1 || preview.CredentialPaths[0] != custom {
		t.Fatal("explicit credential format detection failed", err)
	}
	writeOpenCodeFixture(t, custom, `{"openai":`)
	if _, err := ReadOpenCodeImport(context.Background(), "", custom); err == nil {
		t.Fatal("malformed legacy credentials accepted")
	}
}

func TestOpenCodeJSONC(t *testing.T) {
	for _, input := range []string{
		`{"url":"https://host/a//b/*c*/",/*comment*/"quote":"\\\"", "items":[1,2,],}`,
		"/* start */ {\n// line\n\"value\": true,\n} // last",
		`{"value":1}/* end */`,
	} {
		if !json.Valid(opencodeconfig.JSON([]byte(input))) {
			t.Fatalf("valid JSONC rejected: %s", input)
		}
	}
	for _, input := range []string{`{} /*`, `{,}`, `{"x":,}`, `{"x":"unfinished}`, `{"x":1} extra`} {
		if json.Valid(opencodeconfig.JSON([]byte(input))) {
			t.Fatalf("invalid JSONC accepted: %s", input)
		}
	}
}

func writeOpenCodeAuth(t *testing.T, path, contents string) {
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

func TestOpenCodeImportRejectsInvalidFiles(t *testing.T) {
	config, auth := openCodeImportFiles(t)
	if _, err := ReadOpenCodeImport(context.Background(), config, ""); err == nil {
		t.Fatal("explicit missing config accepted")
	}
	if _, err := ReadOpenCodeImport(context.Background(), "", auth); err == nil {
		t.Fatal("explicit missing auth accepted")
	}
	writeOpenCodeFixture(t, config, `{"providers":{} /*`)
	if _, err := ReadOpenCodeImport(context.Background(), "", ""); err == nil {
		t.Fatal("malformed config accepted")
	}
	if _, err := ReadOpenCodeImport(context.Background(), "relative.json", ""); err == nil {
		t.Fatal("relative path accepted")
	}
}
