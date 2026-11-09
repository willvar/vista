package server

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"vista/internal/tools"
)

type skillFileEntry struct {
	Path      string `json:"path"`
	Directory bool   `json:"directory,omitempty"`
	Bytes     int64  `json:"bytes"`
}

func skillFileEntries(filesystem fs.FS) ([]skillFileEntry, error) {
	entries := []skillFileEntry{}
	err := fs.WalkDir(filesystem, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || name == "." {
			return err
		}
		if name == tools.SkillDisabledFile || strings.HasPrefix(entry.Name(), ".vista-edit-") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		entries = append(entries, skillFileEntry{Path: name, Directory: entry.IsDir(), Bytes: info.Size()})
		return nil
	})
	return entries, err
}

// Every request remains inside one skill. Reject symlinks even when their
// current target is internal; os.Root also contains concurrent path changes.
func checkSkillFile(root *os.Root, name string) error {
	parts := strings.Split(name, "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 || i == len(parts)-1 && !info.Mode().IsRegular() {
			return fs.ErrPermission
		}
	}
	return nil
}

func skillText(content []byte) bool {
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return false
	}
	for _, c := range content {
		if c < 32 && c != '\n' && c != '\r' && c != '\t' && c != '\f' {
			return false
		}
	}
	return true
}

func serveSkillFile(w http.ResponseWriter, r *http.Request, filesystem fs.FS, root *os.Root) {
	name := r.URL.Query().Get("file")
	if !validSkillPath(name) || strings.HasPrefix(path.Base(name), ".vista-edit-") {
		http.Error(w, "invalid skill file path", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodPut && root == nil {
		http.Error(w, "system skills are read-only and updated with Vista", http.StatusForbidden)
		return
	}
	if root != nil {
		if err := checkSkillFile(root, name); err != nil {
			if errors.Is(err, fs.ErrPermission) {
				http.Error(w, "skill files must be regular files inside this skill", http.StatusForbidden)
			} else {
				skillHTTPError(w, err)
			}
			return
		}
	}
	file, err := filesystem.Open(name)
	if err != nil {
		skillHTTPError(w, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "skill path is not a regular file", http.StatusBadRequest)
		return
	}
	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		skillHTTPError(w, err)
		return
	}
	head = head[:n]
	contentType := http.DetectContentType(head)
	image := contentType == "image/png" || contentType == "image/jpeg" || contentType == "image/gif" || contentType == "image/webp" || contentType == "image/bmp"
	if r.Method == http.MethodGet && r.URL.Query().Get("preview") == "1" {
		if !image {
			http.Error(w, "only raster images can be previewed", http.StatusBadRequest)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": path.Base(name)}))
		reader, ok := file.(io.ReadSeeker)
		if !ok {
			http.Error(w, "skill file cannot be read", http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, path.Base(name), info.ModTime(), reader)
		return
	}
	// Binary previews never turn a large asset into a JSON/base64 allocation.
	var content []byte
	text := !image && (strings.HasPrefix(contentType, "text/") || len(head) == 0)
	if text {
		tail, err := io.ReadAll(file)
		if err != nil {
			skillHTTPError(w, err)
			return
		}
		content = append(head, tail...)
		text = skillText(content)
	}
	if r.Method == http.MethodGet {
		kind := "binary"
		if text {
			kind = "text"
		} else if image {
			kind = "image"
		}
		result := map[string]any{"path": name, "kind": kind, "bytes": info.Size(), "mime": contentType}
		if text {
			result["content"], result["sha256"] = string(content), skillDigest(content)
		}
		jsonOut(w, result)
		return
	}
	if !text {
		http.Error(w, "only UTF-8 text files can be edited", http.StatusBadRequest)
		return
	}
	var input struct {
		Content string `json:"content"`
		SHA256  string `json:"sha256"`
	}
	if err := decodeSettingsRequest(r.Body, &input); err != nil || !skillText([]byte(input.Content)) {
		http.Error(w, "invalid UTF-8 text content", http.StatusBadRequest)
		return
	}
	if name == "SKILL.md" {
		if _, _, err := tools.SkillMetadata(input.Content); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if input.SHA256 == "" || skillDigest(content) != input.SHA256 {
		http.Error(w, name+" changed since it was opened; reopen it before saving", http.StatusConflict)
		return
	}
	parent, err := root.OpenRoot(path.Dir(name))
	if err != nil {
		skillHTTPError(w, err)
		return
	}
	defer parent.Close()
	temporary := ".vista-edit-" + uuid.NewString()
	defer parent.Remove(temporary)
	// Preserve executable permissions on supporting scripts.
	output, err := parent.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err == nil {
		err = output.Chmod(info.Mode().Perm())
		if err == nil {
			_, err = io.WriteString(output, input.Content)
		}
		closeErr := output.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err == nil {
		err = parent.Rename(temporary, path.Base(name))
	}
	if err != nil {
		skillHTTPError(w, err)
		return
	}
	jsonOut(w, map[string]string{"sha256": skillDigest([]byte(input.Content))})
}
