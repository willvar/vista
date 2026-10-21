package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vista/internal/agentconfig"
)

func TestAgentSettingsAPI(t *testing.T) {
	app := &App{dir: t.TempDir(), configDir: t.TempDir()}
	if err := os.Chmod(app.configDir, 0700); err != nil {
		t.Fatal(err)
	}
	request := func(method, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		app.agentSettings(recorder, httptest.NewRequest(method, "/api/agent-settings", strings.NewReader(body)))
		return recorder
	}
	result := request(http.MethodGet, "")
	var initial struct {
		InstructionsPath string `json:"instructionsPath"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &initial); err != nil || result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"maxBytes":4096`) || initial.InstructionsPath != filepath.Join(app.configDir, "AGENTS.md") {
		t.Fatalf("initial settings: %d %s", result.Code, result.Body.String())
	}
	if result := request(http.MethodPut, `{"instructions":"Reply in Chinese."}`); result.Code != http.StatusNoContent {
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
	for _, body := range []string{`{}`, `{"instructions":"x","other":true}`, `{"instructions":"` + strings.Repeat("x", 4097) + `"}`} {
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
		app.agentSettings(recorder, httptest.NewRequest(method, "/api/agent-settings", strings.NewReader(body)))
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
		if result := request(http.MethodPut, update.body); result.Code != http.StatusNoContent {
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
	if result := request(http.MethodPut, `{"instructions":"`+strings.Repeat("x", agentconfig.MaxInstructionsBytes+1)+`","delegationDepth":2}`); result.Code != http.StatusBadRequest {
		t.Fatalf("oversized instructions accepted: %d %s", result.Code, result.Body.String())
	}
	check("retained", 0)
}
