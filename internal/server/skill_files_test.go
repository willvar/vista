package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"vista/internal/tools"
)

func TestSkillSupportingFilesReadEditAndPreservePermissions(t *testing.T) {
	a := &App{configDir: t.TempDir()}
	main := "---\nname: Example\ndescription: Example skill\n---\nRead scripts/run.sh.\n"
	seedSkillFiles(t, a, "example", [][2]string{{"SKILL.md", main}, {"scripts/run.sh", "#!/bin/sh\necho before\n"}, {"references/中文.md", "中文参考。\n"}, {"assets/data.bin", "\x00\xff\x80"}})
	script := filepath.Join(a.configDir, "skills", "example", "scripts", "run.sh")
	if err := os.Chmod(script, 0750); err != nil {
		t.Fatal(err)
	}
	endpoint := "/skills/example?file=" + url.QueryEscape("scripts/run.sh")
	var document struct{ Content, SHA256, Kind string }
	if err := json.Unmarshal(mcpAPI(t, a, "GET", endpoint, nil, 200).Body.Bytes(), &document); err != nil || document.Content != "#!/bin/sh\necho before\n" || document.Kind != "text" {
		t.Fatalf("script document: %+v %v", document, err)
	}
	updated := "#!/bin/sh\necho after\n"
	response := mcpAPI(t, a, "PUT", endpoint, map[string]string{"content": updated, "sha256": document.SHA256}, 200)
	if !strings.Contains(response.Body.String(), skillDigest([]byte(updated))) {
		t.Fatal("save did not return the committed revision")
	}
	info, err := os.Stat(script)
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0750 {
		t.Fatalf("script permissions lost: %v %v", info, err)
	}
	mcpAPI(t, a, "PUT", endpoint, map[string]string{"content": "stale overwrite", "sha256": document.SHA256}, 409)
	if got, _ := os.ReadFile(script); string(got) != updated {
		t.Fatal("stale save changed the script")
	}
	mcpAPI(t, a, "PUT", "/skills/example?file=SKILL.md", map[string]string{"content": "invalid metadata", "sha256": skillDigest([]byte(main))}, 400)
	mcpAPI(t, a, "PUT", "/skills/example?file=missing.txt", map[string]string{"content": "new", "sha256": "missing"}, 404)
	tree := mcpAPI(t, a, "GET", "/skills/example?tree=1", nil, 200).Body.String()
	if !strings.Contains(tree, `"path":"scripts","directory":true`) || !strings.Contains(tree, `references/中文.md`) || strings.Contains(tree, "echo after") || strings.Contains(tree, `"content"`) {
		t.Fatalf("tree must contain metadata only: %s", tree)
	}
}

func TestSkillBinaryReadsAreMetadataOnlyAndDownloadsSupportRanges(t *testing.T) {
	a := &App{configDir: t.TempDir()}
	image := "\x89PNG\r\n\x1a\n" + strings.Repeat("binary payload", 1024)
	seedSkillFiles(t, a, "example", [][2]string{{"SKILL.md", "---\nname: Example\ndescription: Example\n---\n"}, {"assets/image.png", image}, {"assets/raw.bin", "\x00\xff\x80"}})
	for _, file := range []string{"assets/image.png", "assets/raw.bin"} {
		body := mcpAPI(t, a, "GET", "/skills/example?file="+url.QueryEscape(file), nil, 200).Body.String()
		if strings.Contains(body, `"content"`) || strings.Contains(body, "binary payload") {
			t.Fatalf("binary copied into metadata: %s", body)
		}
		mcpAPI(t, a, "PUT", "/skills/example?file="+url.QueryEscape(file), map[string]string{"content": "overwrite", "sha256": "invalid"}, 400)
	}
	r := httptest.NewRequest("GET", "/api/skills/example?file=assets%2Fimage.png&raw=1&preview=1", nil)
	r.Header.Set("Range", "bytes=0-7")
	w := httptest.NewRecorder()
	a.skillSetting(w, r)
	if w.Code != http.StatusPartialContent || w.Body.String() != image[:8] || w.Header().Get("Content-Type") != "image/png" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "inline") || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("image range: %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	main := mcpAPI(t, a, "GET", "/skills/example?file=SKILL.md&raw=1", nil, 200)
	if !strings.HasPrefix(main.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("text download was served as active content")
	}
}

func TestSkillFileScopeAndSystemReadOnly(t *testing.T) {
	a := &App{configDir: t.TempDir()}
	seedSkillFiles(t, a, "example", [][2]string{{"SKILL.md", "---\nname: Example\ndescription: Example\n---\n"}, {"inside.txt", "retained"}})
	for _, name := range []string{"../outside.txt", "/outside.txt", "scripts/../../outside", tools.SkillDisabledFile, "a\\b", ""} {
		mcpAPI(t, a, "GET", "/skills/example?file="+url.QueryEscape(name), nil, 400)
	}
	for _, method := range []string{"DELETE", "PATCH", "POST"} {
		mcpAPI(t, a, method, "/skills/example?file=inside.txt", nil, 405)
	}
	if got, _ := os.ReadFile(filepath.Join(a.configDir, "skills/example/inside.txt")); string(got) != "retained" {
		t.Fatal("file endpoint deleted the skill")
	}
	view := mcpAPI(t, a, "GET", "/skills/system:vista-guide?file=SKILL.md", nil, 200)
	if !strings.Contains(view.Body.String(), `"kind":"text"`) {
		t.Fatal("embedded text is unavailable")
	}
	mcpAPI(t, a, "PUT", "/skills/system:vista-guide?file=SKILL.md", map[string]string{"content": "overwrite"}, 403)
	root := filepath.Join(a.configDir, "skills", "example")
	if err := os.Symlink(filepath.Join(root, "inside.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	mcpAPI(t, a, "GET", "/skills/example?file=link.txt", nil, 403)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "external")); err != nil {
		t.Fatal(err)
	}
	mcpAPI(t, a, "GET", "/skills/example?file=external%2Fsecret.txt", nil, 403)
}
