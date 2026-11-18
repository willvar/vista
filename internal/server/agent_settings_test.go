package server

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vista/internal/agentconfig"
	"vista/internal/configstore"
)

func TestAgentSettingsAPI(t *testing.T) {
	app := &App{dir: t.TempDir(), configDir: t.TempDir()}
	if err := os.Chmod(app.configDir, 0700); err != nil {
		t.Fatal(err)
	}
	request := func(method, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		app.agentSettings(recorder, httptest.NewRequest(method, "/api/agent-settings", strings.NewReader(instructionVersionBody(t, app.configDir, body))))
		return recorder
	}
	result := request(http.MethodGet, "")
	var initial struct {
		InstructionsPath string `json:"instructionsPath"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &initial); err != nil || result.Code != http.StatusOK || initial.InstructionsPath != filepath.Join(app.configDir, "AGENTS.md") {
		t.Fatalf("initial settings: %d %s", result.Code, result.Body.String())
	}
	if result := request(http.MethodPut, `{"instructions":"Reply in Chinese."}`); result.Code != http.StatusOK {
		t.Fatalf("save settings: %d %s", result.Code, result.Body.String())
	}
	if _, err := os.Stat(filepath.Join(app.configDir, "AGENTS.md")); err != nil {
		t.Fatalf("instructions missing from configuration directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(app.dir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("instructions written to state directory: %v", err)
	}
	if result := request(http.MethodGet, ""); result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "Reply in Chinese.") || result.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("saved settings: %d %s", result.Code, result.Body.String())
	}
	for _, body := range []string{`{}`, `{"instructions":"x","other":true}`} {
		if result := request(http.MethodPut, body); result.Code != http.StatusBadRequest {
			t.Fatalf("invalid settings accepted: %d %s", result.Code, result.Body.String())
		}
	}
	if result := request(http.MethodGet, ""); !strings.Contains(result.Body.String(), "Reply in Chinese.") {
		t.Fatalf("invalid save changed settings: %s", result.Body.String())
	}
}

func TestAgentSettingsDelegationDepthAPI(t *testing.T) {
	app := &App{configDir: t.TempDir()}
	if err := os.Chmod(app.configDir, 0700); err != nil {
		t.Fatal(err)
	}
	request := func(method, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		app.agentSettings(recorder, httptest.NewRequest(method, "/api/agent-settings", strings.NewReader(instructionVersionBody(t, app.configDir, body))))
		return recorder
	}
	check := func(instructions string, depth int) {
		t.Helper()
		result := request(http.MethodGet, "")
		var body struct {
			Instructions       string `json:"instructions"`
			DelegationDepth    int    `json:"delegationDepth"`
			MaxDelegationDepth int    `json:"maxDelegationDepth"`
		}
		if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil || result.Code != http.StatusOK || body.Instructions != instructions || body.DelegationDepth != depth || body.MaxDelegationDepth != agentconfig.MaxDelegationDepth {
			t.Fatalf("settings: %d %+v error=%v", result.Code, body, err)
		}
	}
	check("", agentconfig.DefaultDelegationDepth)
	for _, update := range []struct {
		body         string
		instructions string
		depth        int
	}{
		{`{"instructions":"original","delegationDepth":2}`, "original", 2},
		{`{"instructions":"changed"}`, "changed", 2},
		{`{"delegationDepth":8}`, "changed", 8},
		{`{"instructions":""}`, "", 8},
		{`{"instructions":"retained","delegationDepth":1}`, "retained", 1},
		{`{"delegationDepth":0}`, "retained", 0},
	} {
		want := http.StatusNoContent
		if strings.Contains(update.body, `"instructions"`) {
			want = http.StatusOK
		}
		if result := request(http.MethodPut, update.body); result.Code != want {
			t.Fatalf("update %s: %d %s", update.body, result.Code, result.Body.String())
		}
		check(update.instructions, update.depth)
	}
	for _, depth := range []string{`-1`, `9`, `2.5`, `"2"`, `null`, `true`, `{}`, `[]`, `1e20`} {
		body := `{"instructions":"should not change","delegationDepth":` + depth + `}`
		if result := request(http.MethodPut, body); result.Code != http.StatusBadRequest {
			t.Fatalf("invalid depth %s accepted: %d %s", depth, result.Code, result.Body.String())
		}
		check("retained", 0)
	}
	check("retained", 0)
}

func TestAgentSettingsPreserveLongExternalAndEditedInstructions(t *testing.T) {
	app := &App{configDir: t.TempDir()}
	if err := os.Chmod(app.configDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := agentconfig.InstructionsPath(app.configDir)
	instructions := strings.Repeat("保留完整规则。\n", 2000)
	if err := os.WriteFile(path, []byte(instructions), 0600); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []bool{false, true} {
		if edit {
			instructions += strings.Repeat("Additional instructions.\n", 2000)
			body, err := json.Marshal(map[string]any{"instructions": instructions, "delegationDepth": 0})
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			app.agentSettings(w, httptest.NewRequest(http.MethodPut, "/api/agent-settings", strings.NewReader(instructionVersionBody(t, app.configDir, string(body)))))
			if w.Code != http.StatusOK {
				t.Fatalf("long instructions were rejected: %d %s", w.Code, w.Body.String())
			}
		}
		w := httptest.NewRecorder()
		app.agentSettings(w, httptest.NewRequest(http.MethodGet, "/api/agent-settings", nil))
		var got struct{ Instructions string }
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK || got.Instructions != instructions {
			t.Fatalf("long instructions were not returned intact: status=%d error=%v", w.Code, err)
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != instructions {
			t.Fatalf("instructions changed on disk: %v", err)
		}
	}
}

func TestAgentSettingsReadErrorsIdentifyFailedDocument(t *testing.T) {
	for _, tc := range []struct{ file, content, want string }{
		{"AGENTS.md", "invalid UTF-8: \xff", "could not read global instructions: agent instructions must be UTF-8"},
		{configstore.Filename, "{", "could not read agent settings:"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			app := &App{configDir: t.TempDir()}
			if err := os.Chmod(app.configDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(app.configDir, tc.file), []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			app.agentSettings(w, httptest.NewRequest(http.MethodGet, "/api/agent-settings", nil))
			if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), tc.want) {
				t.Fatalf("unhelpful read error: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

// Tests that write instructions carry the version they just read. Dedicated
// stale-write tests below intentionally retain an earlier version.
func instructionVersionBody(t *testing.T, directory, body string) string {
	t.Helper()
	var input map[string]any
	if json.Unmarshal([]byte(body), &input) != nil {
		return body
	}
	if _, ok := input["instructions"]; !ok {
		return body
	}
	data, err := os.ReadFile(filepath.Join(directory, "AGENTS.md"))
	if os.IsNotExist(err) {
		input["expectedSha256"] = "missing"
	} else if err != nil {
		t.Fatal(err)
	} else {
		input["expectedSha256"] = fmt.Sprintf("%x", sha256.Sum256(data))
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestInstructionSettingsRejectStaleOrUnversionedEdits(t *testing.T) {
	app := &App{configDir: t.TempDir(), dir: t.TempDir()}
	if err := os.Chmod(app.configDir, 0700); err != nil {
		t.Fatal(err)
	}
	request := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		app.agentSettings(w, httptest.NewRequest(http.MethodPut, "/api/agent-settings", strings.NewReader(body)))
		return w
	}
	if w := request(`{"instructions":"Unversioned"}`); w.Code != http.StatusBadRequest {
		t.Fatal(w.Code, w.Body.String())
	}
	w := request(`{"instructions":"First rule.","expectedSha256":"missing"}`)
	var first struct {
		InstructionsSHA256 string `json:"instructionsSha256"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &first) != nil || first.InstructionsSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte("First rule."))) {
		t.Fatal(w.Code, w.Body.String())
	}
	// A model/file writer shares the very same AGENTS.md, not a second store.
	if err := os.WriteFile(filepath.Join(app.configDir, "AGENTS.md"), []byte("Changed by another writer."), 0600); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"instructions": "Stale draft", "expectedSha256": first.InstructionsSHA256, "delegationDepth": 3})
	if w := request(string(body)); w.Code != http.StatusConflict {
		t.Fatal(w.Code, w.Body.String())
	}
	if text, _ := agentconfig.Read(app.configDir); text != "Changed by another writer." {
		t.Fatal("stale draft overwrote current rules")
	}
	settings, err := agentconfig.ReadSettings(app.configDir)
	if err != nil || settings.DelegationDepth != agentconfig.DefaultDelegationDepth {
		t.Fatal("conflicting instruction save changed other settings", err)
	}
}
