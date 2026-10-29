package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"vista/internal/mcpclient"
	"vista/internal/modelcatalog"
	"vista/internal/opencodeconfig"
	"vista/internal/workerproto"
)

type extensionImportInput struct {
	Source      string   `json:"source"`
	Kind        string   `json:"kind"`
	Action      string   `json:"action"`
	ConfigPath  string   `json:"configPath"`
	Directory   string   `json:"directory"`
	Fingerprint string   `json:"fingerprint"`
	IDs         []string `json:"ids"`
}

type extensionImportEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Location    string `json:"location,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Warning     string `json:"warning,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`
	FileCount   int    `json:"fileCount,omitempty"`
	Bytes       int64  `json:"bytes,omitempty"`
	config      workerproto.MCPServer
	skillRoot   string
	skillFolder string
	skillDigest string
}

type extensionImportPreview struct {
	ConfigPaths []string               `json:"configPaths"`
	Directory   string                 `json:"directory"`
	Fingerprint string                 `json:"fingerprint"`
	Entries     []extensionImportEntry `json:"entries"`
}

func readExtensionSource(input extensionImportInput) (opencodeconfig.Source, extensionImportPreview, hash.Hash, error) {
	source, err := opencodeconfig.Read(input.ConfigPath)
	result := extensionImportPreview{ConfigPaths: []string{}, Entries: []extensionImportEntry{}}
	if err != nil {
		return source, result, nil, err
	}
	directory := input.Directory
	if directory == "" {
		directory, err = os.Getwd()
	} else {
		directory, err = opencodeconfig.Path(directory, source.Home)
	}
	if err != nil || !filepath.IsAbs(directory) {
		return source, result, nil, errors.New("choose an absolute source working directory")
	}
	result.Directory = directory
	fingerprint := sha256.New()
	_, _ = fingerprint.Write([]byte(input.Kind + "\x00" + directory + "\x00"))
	for _, document := range source.Documents {
		result.ConfigPaths = append(result.ConfigPaths, document.Path)
		_, _ = fingerprint.Write([]byte(document.Path + "\x00"))
		_, _ = fingerprint.Write(document.Data)
	}
	return source, result, fingerprint, nil
}

func (a *App) importExtensions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		settingsHTTPError(w, errors.New("method"), http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var input extensionImportInput
	if decodeSettingsRequest(r.Body, &input) != nil || input.Source != "opencode" || input.Kind != "mcp" && input.Kind != "skills" || input.Action != "preview" && input.Action != "import" || len(input.ConfigPath) > 4096 || len(input.Directory) > 4096 {
		settingsHTTPError(w, errors.New("invalid extension import request"), http.StatusBadRequest)
		return
	}
	var preview extensionImportPreview
	var err error
	if input.Kind == "mcp" {
		preview, err = readOpenCodeMCP(input)
	} else {
		preview, err = readOpenCodeSkills(r.Context(), input)
	}
	if err != nil {
		settingsHTTPError(w, err, http.StatusBadRequest)
		return
	}
	if input.Kind == "mcp" {
		a.mcpMu.Lock()
		defer a.mcpMu.Unlock()
		for i := range preview.Entries {
			for _, existing := range a.mcpServers {
				if strings.EqualFold(existing.Name, preview.Entries[i].Name) {
					preview.Entries[i].Reason = "existing"
				}
			}
		}
	} else {
		a.skillsMu.Lock()
		defer a.skillsMu.Unlock()
		for i := range preview.Entries {
			folder := preview.Entries[i].skillFolder
			if folder != "" {
				if _, err := os.Lstat(filepath.Join(a.configDir, "skills", folder)); !errors.Is(err, os.ErrNotExist) {
					preview.Entries[i].Reason = "existing"
				}
			}
		}
	}
	if input.Action == "preview" {
		jsonOut(w, preview)
		return
	}
	if input.Fingerprint == "" || input.Fingerprint != preview.Fingerprint {
		settingsHTTPError(w, modelcatalog.NewSettingsError("settings_conflict", "OpenCode source changed. Read it again before importing.", nil), http.StatusConflict)
		return
	}
	selected := map[string]bool{}
	for _, id := range input.IDs {
		if id == "" || selected[id] {
			settingsHTTPError(w, errors.New("choose unique entries to import"), http.StatusBadRequest)
			return
		}
		selected[id] = true
	}
	entries := []extensionImportEntry{}
	for _, entry := range preview.Entries {
		if selected[entry.ID] {
			if entry.Reason != "" {
				settingsHTTPError(w, modelcatalog.NewSettingsError("settings_conflict", "A selected entry is no longer importable. Read it again.", nil), http.StatusConflict)
				return
			}
			entries = append(entries, entry)
		}
	}
	if len(entries) == 0 || len(entries) != len(selected) {
		settingsHTTPError(w, errors.New("choose entries from the current preview"), http.StatusBadRequest)
		return
	}
	if err := r.Context().Err(); err != nil {
		settingsHTTPError(w, err, http.StatusBadRequest)
		return
	}
	if input.Kind == "mcp" {
		next := append([]workerproto.MCPServer(nil), a.mcpServers...)
		if len(next)+len(entries) > 4 {
			settingsHTTPError(w, errors.New("at most four MCP servers are supported; select fewer entries"), http.StatusBadRequest)
			return
		}
		for _, entry := range entries {
			config := entry.config
			config.ID = uuid.NewString()
			if err := mcpclient.ValidateConfig(config); err != nil {
				settingsHTTPError(w, errors.New("an imported MCP configuration is invalid"), http.StatusBadRequest)
				return
			}
			next = append(next, config)
		}
		if err := writeMCPConfig(a.configDir, next); err != nil {
			settingsHTTPError(w, err, http.StatusInternalServerError)
			return
		}
		a.mcpServers = next
		a.refreshMCPHost()
		a.updates.notify("mcp")
	} else if err := a.copyImportedSkills(r.Context(), entries); err != nil {
		settingsHTTPError(w, err, http.StatusBadRequest)
		return
	}
	jsonOut(w, map[string]int{"imported": len(entries)})
}

func finishExtensionPreview(result extensionImportPreview, fingerprint hash.Hash) extensionImportPreview {
	// Resolved environment/file references affect the confirmation fingerprint,
	// while private configuration fields never appear in its JSON projection.
	for _, entry := range result.Entries {
		data, _ := json.Marshal(entry.config)
		_, _ = fingerprint.Write(data)
		_, _ = fingerprint.Write([]byte(entry.ID + "\x00" + entry.Reason + "\x00" + entry.skillDigest + "\x00"))
	}
	result.Fingerprint = hex.EncodeToString(fingerprint.Sum(nil))
	return result
}
