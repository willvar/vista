package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"vista/internal/tools"
)

func validSkillPath(path string) bool {
	if !fs.ValidPath(path) || path == "." || strings.ContainsAny(path, `\:*?"<>|`) {
		return false
	}
	for _, c := range path {
		if c < 32 || c == 127 {
			return false
		}
	}
	for _, part := range strings.Split(path, "/") {
		if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9') {
			return false
		}
	}
	return strings.Split(path, "/")[0] != tools.SkillDisabledFile
}

func (a *App) openSkillsRoot() (*os.Root, error) {
	config, err := os.OpenRoot(a.configDir)
	if err != nil {
		return nil, err
	}
	defer config.Close()
	info, err := config.Lstat("skills")
	if errors.Is(err, os.ErrNotExist) {
		if err = config.Mkdir("skills", 0700); err != nil {
			return nil, err
		}
		info, err = config.Lstat("skills")
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("skills must be a directory, not a symbolic link")
	}
	return config.OpenRoot("skills")
}

func (a *App) skillSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	a.skillsMu.Lock()
	defer a.skillsMu.Unlock()
	entries := []tools.SkillEntry{}
	root, err := a.openSkillsRoot()
	if err == nil {
		defer root.Close()
		entries, err = tools.ListSkills(root.Name())
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOut(w, map[string]any{"skills": entries, "directory": filepath.Join(a.configDir, "skills"), "maxSkills": tools.MaxSkills, "maxBytes": tools.MaxSkillBytes})
}

func (a *App) skillSetting(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := strings.TrimPrefix(r.URL.Path, "/api/skills/")
	if !validSkillPath(id) || strings.Contains(id, "/") || strings.HasPrefix(id, ".") {
		http.Error(w, "invalid skill folder name", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPut && r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	// Settings mutations are infrequent; one lock keeps toggles and edits
	// atomic with respect to other WebUI requests, without a database registry.
	a.skillsMu.Lock()
	defer a.skillsMu.Unlock()
	root, err := a.openSkillsRoot()
	if err != nil {
		skillHTTPError(w, err)
		return
	}
	defer root.Close()
	info, statErr := root.Lstat(id)
	if statErr != nil {
		skillHTTPError(w, statErr)
		return
	}
	if !info.IsDir() {
		http.Error(w, "skill folder must be a directory, not a symbolic link", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodDelete {
		if err := root.RemoveAll(id); err != nil {
			skillHTTPError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	skill, err := root.OpenRoot(id)
	if err != nil {
		skillHTTPError(w, err)
		return
	}
	defer skill.Close()
	switch r.Method {
	case http.MethodGet:
		content, err := readManagedSkill(skill)
		if err != nil {
			skillHTTPError(w, err)
			return
		}
		files := []string{}
		err = fs.WalkDir(skill.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && path != "SKILL.md" && path != tools.SkillDisabledFile {
				files = append(files, path)
			}
			return r.Context().Err()
		})
		if err != nil {
			skillHTTPError(w, err)
			return
		}
		jsonOut(w, map[string]any{"content": string(content), "sha256": skillDigest(content), "files": files})
	case http.MethodPut:
		r.Body = http.MaxBytesReader(w, r.Body, 8*tools.MaxSkillBytes)
		var input struct {
			Content string `json:"content"`
			SHA256  string `json:"sha256"`
		}
		if err := decodeSettingsRequest(r.Body, &input); err != nil {
			http.Error(w, "invalid skill content", http.StatusBadRequest)
			return
		}
		if _, _, err := tools.SkillMetadata(input.Content); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		content, err := readManagedSkill(skill)
		if err != nil {
			skillHTTPError(w, err)
			return
		}
		if input.SHA256 == "" || skillDigest(content) != input.SHA256 {
			http.Error(w, "SKILL.md changed since it was opened; reopen it before saving", http.StatusConflict)
			return
		}
		// Rename a complete file; Agent readers see either the old or new content.
		tmp := ".vista-edit-" + uuid.NewString()
		defer skill.Remove(tmp)
		if err := skill.WriteFile(tmp, []byte(input.Content), 0600); err != nil {
			skillHTTPError(w, err)
			return
		}
		if err := skill.Rename(tmp, "SKILL.md"); err != nil {
			skillHTTPError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPatch:
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		var input struct {
			Disabled *bool `json:"disabled"`
		}
		if err := decodeSettingsRequest(r.Body, &input); err != nil || input.Disabled == nil {
			http.Error(w, "disabled is required", http.StatusBadRequest)
			return
		}
		if *input.Disabled {
			file, err := skill.OpenFile(tools.SkillDisabledFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err == nil {
				err = file.Close()
			}
			if err != nil && !errors.Is(err, os.ErrExist) {
				skillHTTPError(w, err)
				return
			}
		} else if err := skill.Remove(tools.SkillDisabledFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			skillHTTPError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func readManagedSkill(root *os.Root) ([]byte, error) {
	info, err := root.Lstat("SKILL.md")
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > tools.MaxSkillBytes {
		return nil, errors.New("SKILL.md must be a regular file of at most 32 KiB")
	}
	file, err := root.Open("SKILL.md")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	b, err := io.ReadAll(io.LimitReader(file, tools.MaxSkillBytes+1))
	if err != nil {
		return nil, err
	}
	// Invalid frontmatter remains editable; the save path validates the repair.
	if len(b) > tools.MaxSkillBytes || !utf8.Valid(b) {
		return nil, errors.New("SKILL.md exceeds 32 KiB or is not UTF-8 text")
	}
	return b, nil
}

func skillDigest(b []byte) string {
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}

func skillHTTPError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, os.ErrNotExist) {
		status = http.StatusNotFound
	}
	http.Error(w, err.Error(), status)
}
