package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"vista/internal/workerproto"
)

func extensionSource(t *testing.T, kind string, config map[string]any) (extensionImportInput, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.jsonc")
	data, _ := json.Marshal(config)
	if err := os.WriteFile(path, append([]byte("// OpenCode configuration\n"), data...), 0600); err != nil {
		t.Fatal(err)
	}
	return extensionImportInput{Source: "opencode", Kind: kind, ConfigPath: path, Directory: dir}, dir
}

func extensionRequest(t *testing.T, app *App, input extensionImportInput, action string, status int) *httptest.ResponseRecorder {
	t.Helper()
	input.Action = action
	data, _ := json.Marshal(input)
	w := httptest.NewRecorder()
	app.importExtensions(w, httptest.NewRequest(http.MethodPost, "/api/extension-import", bytes.NewReader(data)))
	if w.Code != status {
		t.Fatalf("%s %s: %d %s", input.Kind, action, w.Code, w.Body)
	}
	return w
}

func extensionPreview(t *testing.T, app *App, input *extensionImportInput) extensionImportPreview {
	t.Helper()
	w := extensionRequest(t, app, *input, "preview", 200)
	var result extensionImportPreview
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatal("source secrets exposed in preview")
	}
	input.Fingerprint = result.Fingerprint
	return result
}

func TestMCPImportPreviewsWithoutConnectingAndPreservesCredentialsAndDisabledState(t *testing.T) {
	t.Setenv("VISTA_IMPORT_MCP_KEY", "test-secret")
	command, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input, dir := extensionSource(t, "mcp", map[string]any{"mcp": map[string]any{"servers": map[string]any{
		"local":   map[string]any{"type": "local", "command": []string{command, "--no-run"}, "cwd": "work", "disabled": true},
		"remote":  map[string]any{"type": "remote", "url": "https://example.invalid/mcp?token=endpoint-secret", "headers": map[string]string{"Authorization": "Bearer {env:VISTA_IMPORT_MCP_KEY}"}, "oauth": false},
		"oauth":   map[string]any{"type": "remote", "url": "https://oauth.invalid/mcp", "oauth": map[string]string{"client_id": "client", "client_secret": "oauth-secret"}},
		"env":     map[string]any{"type": "local", "command": []string{command}, "environment": map[string]string{"API_KEY": "env-secret"}},
		"headers": map[string]any{"type": "remote", "url": "https://example.invalid/mcp", "headers": map[string]string{"X-Key": "header-secret"}},
	}}})
	app := &App{configDir: t.TempDir()}
	if err := os.Chmod(app.configDir, 0700); err != nil {
		t.Fatal(err)
	}
	preview := extensionPreview(t, app, &input)
	if len(preview.Entries) != 5 {
		t.Fatalf("incomplete preview: %+v", preview)
	}
	for _, entry := range preview.Entries {
		if entry.Name == "env" {
			if entry.Reason != "unsupported" {
				t.Fatal("unsupported setting silently discarded")
			}
		} else {
			if entry.Reason != "" {
				t.Fatal("valid source rejected", entry.Name, entry.Reason)
			}
			input.IDs = append(input.IDs, entry.ID)
		}
	}
	if _, err := os.Stat(filepath.Join(app.configDir, "mcp.json")); !os.IsNotExist(err) {
		t.Fatal("preview saved MCP settings")
	}
	extensionRequest(t, app, input, "import", 200)
	if len(app.mcpServers) != 4 {
		t.Fatal("selected MCP services not saved")
	}
	for _, config := range app.mcpServers {
		if config.ID == "preview" {
			t.Fatal("preview ID used as persistent identity")
		}
		switch config.Name {
		case "local":
			if config.Directory != filepath.Join(dir, "work") || !config.Disabled || config.Command != command || len(config.Args) != 1 {
				t.Fatal("local configuration changed")
			}
		case "remote":
			if config.Token != "test-secret" || !strings.Contains(config.Endpoint, "endpoint-secret") {
				t.Fatal("remote credentials lost")
			}
		case "oauth":
			if !config.OAuth || config.ClientID != "client" || config.ClientSecret != "oauth-secret" {
				t.Fatal("OAuth settings lost")
			}
		case "headers":
			if config.Headers["X-Key"] != "header-secret" || config.Token != "" {
				t.Fatal("custom authentication header lost or converted to a bearer token")
			}
		}
	}
	preview = extensionPreview(t, app, &input)
	for _, entry := range preview.Entries {
		if entry.Name != "env" {
			if entry.Reason != "existing" {
				t.Fatal("existing service not excluded")
			}
		}
	}
}

func TestMCPImportRejectsChangedReferencesAndNeverOverwritesExistingServices(t *testing.T) {
	t.Setenv("VISTA_IMPORT_MCP_ENDPOINT", "https://one.invalid/mcp")
	input, _ := extensionSource(t, "mcp", map[string]any{"mcp": map[string]any{"servers": map[string]any{"imported": map[string]any{"type": "remote", "url": "{env:VISTA_IMPORT_MCP_ENDPOINT}", "oauth": false}}}})
	app := &App{configDir: t.TempDir(), mcpServers: []workerproto.MCPServer{{ID: "existing", Name: "Original", Transport: "http", Endpoint: "https://original.invalid/mcp"}}}
	before := append([]workerproto.MCPServer(nil), app.mcpServers...)
	preview := extensionPreview(t, app, &input)
	input.IDs = []string{preview.Entries[0].ID}
	t.Setenv("VISTA_IMPORT_MCP_ENDPOINT", "https://two.invalid/mcp")
	extensionRequest(t, app, input, "import", 409)
	if !reflect.DeepEqual(app.mcpServers, before) {
		t.Fatal("failed import changed existing settings")
	}
	preview = extensionPreview(t, app, &input)
	input.IDs = []string{preview.Entries[0].ID, preview.Entries[0].ID}
	extensionRequest(t, app, input, "import", 400)
	input.IDs = []string{preview.Entries[0].ID}
	app.mcpServers = append(app.mcpServers, workerproto.MCPServer{ID: "new", Name: "imported", Transport: "http", Endpoint: "https://keep.invalid/mcp"})
	extensionRequest(t, app, input, "import", 409)
	if app.mcpServers[1].Endpoint != "https://keep.invalid/mcp" {
		t.Fatal("destination overwritten")
	}
}

func TestSkillsImportCopiesCompleteFoldersAndRejectsChangedBinaryAssets(t *testing.T) {
	input, dir := extensionSource(t, "skills", map[string]any{})
	source := filepath.Join(dir, "skills", "review")
	for _, sub := range []string{"assets", "scripts", "references"} {
		if err := os.MkdirAll(filepath.Join(source, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	content := "---\nname: Review\ndescription: Inspect changes\n---\nRead references/check.md."
	asset := bytes.Repeat([]byte{0, 255, 128}, 400000)
	files := map[string][]byte{"SKILL.md": []byte(content), "assets/image.bin": asset, "scripts/check.sh": []byte("#!/bin/sh\nexit 0\n"), "references/check.md": []byte("Inspect real evidence")}
	for path, data := range files {
		mode := os.FileMode(0600)
		if strings.HasPrefix(path, "scripts/") {
			mode = 0700
		}
		if err := os.WriteFile(filepath.Join(source, filepath.FromSlash(path)), data, mode); err != nil {
			t.Fatal(err)
		}
	}
	app := &App{configDir: t.TempDir()}
	preview := extensionPreview(t, app, &input)
	if len(preview.Entries) != 1 || preview.Entries[0].Reason != "" || preview.Entries[0].FileCount != 4 {
		t.Fatalf("incomplete skill preview: %+v", preview)
	}
	input.IDs = []string{preview.Entries[0].ID}
	if _, err := os.Stat(filepath.Join(app.configDir, "skills")); !os.IsNotExist(err) {
		t.Fatal("preview wrote skill files")
	}
	if err := os.WriteFile(filepath.Join(source, "assets/image.bin"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	extensionRequest(t, app, input, "import", 409)
	if err := os.WriteFile(filepath.Join(source, "assets/image.bin"), asset, 0600); err != nil {
		t.Fatal(err)
	}
	extensionRequest(t, app, input, "import", 200)
	for path, expected := range files {
		got, err := os.ReadFile(filepath.Join(app.configDir, "skills", "review", filepath.FromSlash(path)))
		if err != nil || !bytes.Equal(got, expected) {
			t.Fatal("supporting file changed or missing", path, err)
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(app.configDir, "skills/review/scripts/check.sh"))
		if err != nil || info.Mode().Perm()&0100 == 0 {
			t.Fatal("script execution permission lost")
		}
	}
	paths, _ := filepath.Glob(filepath.Join(app.configDir, "skills", ".vista-import-*"))
	if len(paths) > 0 {
		t.Fatal("temporary import files left behind")
	}
	preview = extensionPreview(t, app, &input)
	if preview.Entries[0].Reason != "existing" {
		t.Fatal("existing user skill not excluded")
	}
	extensionRequest(t, app, input, "import", 409)
}

func TestSkillsImportAcceptsBothConfiguredPathFormats(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		t.Run(map[bool]string{true: "v1", false: "v2"}[legacy], func(t *testing.T) {
			var sources any = []string{"custom-skills", "https://example.invalid/skills"}
			if legacy {
				sources = map[string]any{"paths": []string{"custom-skills"}, "urls": []string{"https://example.invalid/skills"}}
			}
			input, dir := extensionSource(t, "skills", map[string]any{"skills": sources, "provider": map[string]any{}})
			folder := filepath.Join(dir, "custom-skills", "review")
			if err := os.MkdirAll(folder, 0700); err != nil {
				t.Fatal(err)
			}
			content := "---\nname: Review\ndescription: Review the task\n---\nInspect the evidence."
			if err := os.WriteFile(filepath.Join(folder, "SKILL.md"), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			app := &App{configDir: t.TempDir()}
			preview := extensionPreview(t, app, &input)
			if len(preview.Entries) != 2 {
				t.Fatal("configured local and remote sources not both represented")
			}
			for _, entry := range preview.Entries {
				if entry.Name == "Review" {
					if entry.Reason != "" {
						t.Fatal("configured local skill rejected", entry.Reason)
					}
					input.IDs = []string{entry.ID}
				} else if entry.Reason != "remote" {
					t.Fatal("remote source was not identified as unsupported")
				}
			}
			extensionRequest(t, app, input, "import", 200)
			if data, err := os.ReadFile(filepath.Join(app.configDir, "skills", "review", "SKILL.md")); err != nil || string(data) != content {
				t.Fatal("configured skill was not copied intact", err)
			}
		})
	}
}

func TestExtensionImportRejectsInvalidContainers(t *testing.T) {
	for kind, config := range map[string]map[string]any{
		"mcp":    {"mcp": map[string]any{"servers": "invalid"}},
		"skills": {"skills": 42},
	} {
		input, _ := extensionSource(t, kind, config)
		extensionRequest(t, &App{configDir: t.TempDir()}, input, "preview", 400)
	}
}

func TestMCPImportAcceptsOpenCodeV2FlatEntriesAndPrefersNativeEntries(t *testing.T) {
	input, _ := extensionSource(t, "mcp", map[string]any{"mcp": map[string]any{
		"context7": map[string]any{"type": "remote", "url": "https://example.invalid/mcp", "headers": map[string]string{"CONTEXT7_API_KEY": "context-secret"}, "enabled": false},
		"same":     map[string]any{"type": "remote", "url": "https://old.invalid/mcp", "headers": map[string]string{"X-Old": "old-secret"}},
		"oauth":    map[string]any{"type": "remote", "url": "https://auth.invalid/mcp", "oauth": map[string]string{"clientId": "client", "clientSecret": "oauth-secret"}},
		"servers":  map[string]any{"same": map[string]any{"type": "remote", "url": "https://new.invalid/mcp", "oauth": false}},
	}})
	app := &App{configDir: mcpConfigDir(t)}
	preview := extensionPreview(t, app, &input)
	if len(preview.Entries) != 3 {
		t.Fatal("flat and native entries not combined")
	}
	for _, entry := range preview.Entries {
		if entry.Reason != "" {
			t.Fatal("OpenCode v2 accepted configuration rejected", entry.Name, entry.Reason)
		}
		input.IDs = append(input.IDs, entry.ID)
	}
	extensionRequest(t, app, input, "import", 200)
	configs, err := loadMCPConfig(app.configDir)
	if err != nil || len(configs) != 3 {
		t.Fatal("imported configuration did not survive reload", err)
	}
	for _, config := range configs {
		switch config.Name {
		case "context7":
			if !config.Disabled || config.Headers["CONTEXT7_API_KEY"] != "context-secret" || config.Token != "" {
				t.Fatal("flat MCP configuration lost its credentials or disabled state")
			}
		case "same":
			if config.Endpoint != "https://new.invalid/mcp" || len(config.Headers) > 0 {
				t.Fatal("native entry failed to replace the flat entry")
			}
		case "oauth":
			if config.ClientID != "client" || config.ClientSecret != "oauth-secret" || !config.OAuth {
				t.Fatal("flat OAuth client configuration lost")
			}
		}
	}
	view := mcpAPI(t, app, "GET", "/mcp-config", nil, 200)
	if strings.Contains(view.Body.String(), "secret") || !strings.Contains(view.Body.String(), "CONTEXT7_API_KEY") {
		t.Fatal("imported credentials exposed or custom header names hidden")
	}
}

func TestMCPImportPreservesReservedServerNamesAndRejectsUnsupportedTimeouts(t *testing.T) {
	for _, name := range []string{"servers", "timeout"} {
		input, _ := extensionSource(t, "mcp", map[string]any{"mcp": map[string]any{name: map[string]any{"type": "remote", "url": "https://example.invalid/mcp", "enabled": true}}})
		preview := extensionPreview(t, &App{configDir: t.TempDir()}, &input)
		if len(preview.Entries) != 1 || preview.Entries[0].Name != name || preview.Entries[0].Reason != "" || preview.Entries[0].Disabled {
			t.Fatal("server name confused with v2 configuration field")
		}
	}
	for _, timeout := range []any{5000, map[string]any{"execution": 5000}} {
		input, _ := extensionSource(t, "mcp", map[string]any{"mcp": map[string]any{"timeout": timeout, "servers": map[string]any{
			"timed": map[string]any{"type": "remote", "url": "https://example.invalid/mcp"},
		}}})
		preview := extensionPreview(t, &App{configDir: t.TempDir()}, &input)
		if len(preview.Entries) != 1 || preview.Entries[0].Reason != "unsupported" {
			t.Fatal("configured timeout silently discarded")
		}
	}
}
