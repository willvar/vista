package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"vista/internal/tools"
)

func seedSkillFiles(t *testing.T, a *App, id string, files [][2]string) {
	t.Helper()
	for _, file := range files {
		path := filepath.Join(a.configDir, "skills", id, filepath.FromSlash(file[0]))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(file[1]), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSkillsSettingsDiscoverEditToggleAndDelete(t *testing.T) {
	a := &App{configDir: t.TempDir()}
	for _, path := range []string{"/settings/skills", "/settings/mcp", "/settings/about"} {
		w := httptest.NewRecorder()
		a.handler().ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:5186"+path, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "/app.js") {
			t.Fatalf("settings direct URL did not load the application: %s", path)
		}
	}
	empty := mcpAPI(t, a, "GET", "/skills", nil, 200)
	if !strings.Contains(empty.Body.String(), `"skills":[]`) {
		t.Fatalf("unexpected empty settings: %s", empty.Body.String())
	}
	if info, err := os.Stat(filepath.Join(a.configDir, "skills")); err != nil || !info.IsDir() {
		t.Fatalf("skill directory is not ready for local files: %v", err)
	}
	content := "---\nname: Review\ndescription: Review a change\n---\nRead scripts/check.py."
	files := [][2]string{{"SKILL.md", content}, {"scripts/check.py", "print('ok')\n"}, {"assets/sample.bin", "\x00\xff\x80"}}
	seedSkillFiles(t, a, "review", files)
	root := filepath.Join(a.configDir, "skills")
	list, err := tools.DiscoverSkills(root)
	if err != nil || len(list) != 1 || list[0].Name != "Review" {
		t.Fatalf("local skill not available to Agent: %+v %v", list, err)
	}
	if body := mcpAPI(t, a, "GET", "/skills", nil, 200).Body.String(); !strings.Contains(body, `"name":"Review"`) {
		t.Fatalf("settings did not discover local files: %s", body)
	}
	view := mcpAPI(t, a, "GET", "/skills/review", nil, 200)
	var document struct {
		Content string   `json:"content"`
		SHA256  string   `json:"sha256"`
		Files   []string `json:"files"`
	}
	if err := json.Unmarshal(view.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Content != content || !reflect.DeepEqual(document.Files, []string{"assets/sample.bin", "scripts/check.py"}) {
		t.Fatalf("incomplete document: %+v", document)
	}
	mcpAPI(t, a, "PUT", "/skills/review", map[string]string{"content": "---\nname: [invalid\n---\n", "sha256": document.SHA256}, 400)
	updated := "---\nname: Updated review\ndescription: Check tests\n---\nNew instructions"
	mcpAPI(t, a, "PUT", "/skills/review", map[string]string{"content": updated, "sha256": document.SHA256}, 204)
	mcpAPI(t, a, "PUT", "/skills/review", map[string]string{"content": content, "sha256": document.SHA256}, 409)
	for _, file := range files[1:] {
		got, err := os.ReadFile(filepath.Join(root, "review", filepath.FromSlash(file[0])))
		if err != nil || string(got) != file[1] {
			t.Fatalf("editing destroyed supporting file: %s %q %v", file[0], got, err)
		}
	}
	mcpAPI(t, a, "PATCH", "/skills/review", map[string]bool{"disabled": true}, 204)
	mcpAPI(t, a, "PATCH", "/skills/review", map[string]bool{"disabled": true}, 204)
	all, err := tools.ListSkills(root)
	if err != nil || len(all) != 1 || !all[0].Disabled || all[0].Name != "Updated review" {
		t.Fatalf("disabled metadata: %+v %v", all, err)
	}
	if list, err := tools.DiscoverSkills(root); err != nil || len(list) != 0 {
		t.Fatalf("disabled skill available to Agent: %+v %v", list, err)
	}
	// No in-memory registration is needed after restart.
	a = &App{configDir: a.configDir}
	if body := mcpAPI(t, a, "GET", "/skills", nil, 200).Body.String(); !strings.Contains(body, `"disabled":true`) {
		t.Fatalf("disabled state lost: %s", body)
	}
	mcpAPI(t, a, "PATCH", "/skills/review", map[string]bool{"disabled": false}, 204)
	if list, err := tools.DiscoverSkills(root); err != nil || len(list) != 1 || list[0].Name != "Updated review" {
		t.Fatalf("reenabled skill unavailable: %+v %v", list, err)
	}
	mcpAPI(t, a, "DELETE", "/skills/review", nil, 204)
	mcpAPI(t, a, "GET", "/skills/review", nil, 404)
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("deleted skill files remain: %+v %v", entries, err)
	}
}

func TestSkillsSettingsRejectInvalidChanges(t *testing.T) {
	a := &App{configDir: t.TempDir()}
	for _, path := range []string{"/skills/.hidden", "/skills/CON", "/skills/a%5Cb", "/skills/bad.", "/skills/a/b"} {
		mcpAPI(t, a, "GET", path, nil, 400)
	}
	seedSkillFiles(t, a, "valid", [][2]string{{"SKILL.md", "Keep me"}})
	mcpAPI(t, a, "PATCH", "/skills/valid", map[string]string{}, 400)
	mcpAPI(t, a, "PUT", "/skills/valid", map[string]string{"content": "overwrite"}, 409)
	got, err := os.ReadFile(filepath.Join(a.configDir, "skills", "valid", "SKILL.md"))
	if err != nil || string(got) != "Keep me" {
		t.Fatalf("invalid update overwrote skill: %q %v", got, err)
	}
}

func TestSkillsSettingsDoNotFollowExternalSymlinks(t *testing.T) {
	a := &App{configDir: t.TempDir()}
	outside := t.TempDir()
	target := filepath.Join(outside, "SKILL.md")
	if err := os.WriteFile(target, []byte("External content"), 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(a.configDir, "skills")
	if err := os.Symlink(outside, root); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	mcpAPI(t, a, "GET", "/skills", nil, 500)
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "PUT", "PATCH", "DELETE"} {
		mcpAPI(t, a, method, "/skills/linked", nil, 400)
	}
	if err := os.Mkdir(filepath.Join(root, "skill"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "skill", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	mcpAPI(t, a, "GET", "/skills/skill", nil, 500)
	mcpAPI(t, a, "PUT", "/skills/skill", map[string]string{"content": "overwrite", "sha256": skillDigest([]byte("External content"))}, 500)
	mcpAPI(t, a, "DELETE", "/skills/skill", nil, 204)
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "External content" {
		t.Fatalf("external content modified: %q %v", got, err)
	}
}
