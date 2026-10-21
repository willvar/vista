package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"vista/internal/mcpclient"
	"vista/internal/workerproto"

	"vista/internal/platform"
)

type mcpCheck struct {
	Status    string    `json:"status"`
	CheckedAt time.Time `json:"checkedAt,omitzero"`
	ToolCount int       `json:"toolCount"`
	Error     string    `json:"error,omitempty"`
}

type mcpSetting struct {
	workerproto.MCPServer
	HasToken        bool     `json:"hasToken"`
	HasClientSecret bool     `json:"hasClientSecret"`
	Authorized      bool     `json:"authorized"`
	Check           mcpCheck `json:"check"`
}

// Each execution takes an independent snapshot. Saving settings does not change
// connections already owned by a running Agent.
func (a *App) currentMCPServers() []workerproto.MCPServer {
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()
	configs := make([]workerproto.MCPServer, 0, len(a.mcpServers))
	for _, config := range a.mcpServers {
		if !config.Disabled {
			config.Args = append([]string(nil), config.Args...)
			configs = append(configs, config)
		}
	}
	return configs
}

func (a *App) mcpIndexLocked(id string) int {
	for i, config := range a.mcpServers {
		if config.ID == id {
			return i
		}
	}
	return -1
}

// Names are display labels; changing one does not change the connection or its
// authorization, nor invalidate a check or login already in progress.
func sameMCPConnection(a, b workerproto.MCPServer) bool {
	a.Name, b.Name = "", ""
	argsEqual := slices.Equal(a.Args, b.Args)
	a.Args, b.Args = nil, nil
	return argsEqual && reflect.DeepEqual(a, b)
}

func (a *App) mcpSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		a.mcpMu.Lock()
		defer a.mcpMu.Unlock()
		servers := make([]mcpSetting, 0, len(a.mcpServers))
		for _, config := range a.mcpServers {
			item := mcpSetting{MCPServer: config, HasToken: config.Token != "", HasClientSecret: config.ClientSecret != "", Check: a.mcpChecks[config.ID]}
			item.Token, item.ClientSecret = "", ""
			item.Authorized = config.OAuth && mcpclient.OAuthConfigured(a.configDir, config)
			if item.Check.Status == "" {
				item.Check.Status = "unchecked"
			}
			servers = append(servers, item)
		}
		directory, _ := os.Getwd()
		jsonOut(w, map[string]any{"servers": servers, "maxServers": 4, "defaultDirectory": directory})
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var input struct {
		workerproto.MCPServer
		ClearToken        bool `json:"clearToken"`
		ClearClientSecret bool `json:"clearClientSecret"`
	}
	if err := decodeSettingsRequest(r.Body, &input); err != nil {
		http.Error(w, "invalid MCP settings", http.StatusBadRequest)
		return
	}
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()
	if r.Method == http.MethodPost && input.ID == "" {
		input.ID = uuid.NewString()
	}
	if input.ID == "" {
		http.Error(w, "MCP server ID is required", http.StatusBadRequest)
		return
	}
	index := a.mcpIndexLocked(input.ID)
	if r.Method == http.MethodPost && index >= 0 {
		http.Error(w, "MCP server ID already exists", http.StatusConflict)
		return
	}
	if r.Method != http.MethodPost && index < 0 {
		http.Error(w, "MCP server not found", http.StatusNotFound)
		return
	}
	next := append([]workerproto.MCPServer(nil), a.mcpServers...)
	var previous workerproto.MCPServer
	if index >= 0 {
		previous = next[index]
	}
	var current workerproto.MCPServer
	switch r.Method {
	case http.MethodDelete:
		next = append(next[:index], next[index+1:]...)
	case http.MethodPatch:
		next[index].Disabled = input.Disabled
		current = next[index]
	default:
		config := input.MCPServer
		config.Name = strings.TrimSpace(config.Name)
		if config.Transport == "stdio" {
			config.Endpoint, config.Token, config.ClientID, config.ClientSecret, config.OAuth = "", "", "", "", false
		} else {
			config.Command, config.Directory, config.Args = "", "", nil
			if config.OAuth {
				config.Token = ""
				if config.ClientSecret == "" && !input.ClearClientSecret && config.ClientID == previous.ClientID && config.Endpoint == previous.Endpoint && previous.OAuth {
					config.ClientSecret = previous.ClientSecret
				}
			} else {
				config.ClientID, config.ClientSecret = "", ""
				if config.Token == "" && !input.ClearToken && !previous.OAuth {
					config.Token = previous.Token
				}
			}
		}
		if err := mcpclient.ValidateConfig(config); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for i, existing := range next {
			if i != index && existing.Name == config.Name {
				http.Error(w, "MCP server name already exists", http.StatusConflict)
				return
			}
		}
		if index >= 0 {
			next[index] = config
		} else {
			if len(next) >= 4 {
				http.Error(w, "at most four MCP servers are supported", http.StatusBadRequest)
				return
			}
			next = append(next, config)
		}
		current = config
	}
	if err := writeMCPConfig(a.configDir, next); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.mcpServers = next
	changed := !sameMCPConnection(previous, current)
	if changed && r.Method != http.MethodPatch {
		delete(a.mcpChecks, input.ID)
	}
	if changed {
		a.cancelMCPLoginLocked(input.ID)
	} else {
		for _, login := range a.mcpLogins {
			if login.ServerID == input.ID {
				login.Name = current.Name
			}
		}
	}
	// A change in OAuth identity invalidates the saved authorization. Other
	// edits and the enable switch preserve it.
	if previous.OAuth && (!current.OAuth || current.Endpoint != previous.Endpoint || current.ClientID != previous.ClientID || current.ClientSecret != previous.ClientSecret) {
		if err := mcpclient.RemoveOAuth(a.configDir, input.ID); err != nil {
			http.Error(w, "configuration saved, but saved OAuth credentials could not be removed", http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeMCPConfig(dir string, configs []workerproto.MCPServer) error {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || !platform.PrivateMode(info.Mode()) {
		return errors.New("Vista configuration directory must be private")
	}
	if configs == nil {
		configs = []workerproto.MCPServer{}
	}
	data, err := json.MarshalIndent(configs, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > 8<<10 {
		return errors.New("MCP configuration exceeds 8 KiB")
	}
	file, err := os.CreateTemp(dir, ".mcp-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, "mcp.json"))
}

func (a *App) mcpServerAction(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/mcp-servers/"), "/")
	if len(parts) != 2 || (parts[1] != "check" && parts[1] != "login") {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	a.mcpMu.Lock()
	index := a.mcpIndexLocked(parts[0])
	if index < 0 {
		a.mcpMu.Unlock()
		http.NotFound(w, r)
		return
	}
	config := a.mcpServers[index]
	if parts[1] == "login" {
		a.startMCPLoginLocked(w, config)
		a.mcpMu.Unlock()
		return
	}
	a.mcpMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	tools, err := a.checkMCP(ctx, config)
	check := mcpCheck{Status: "available", CheckedAt: time.Now().UTC(), ToolCount: len(tools)}
	if err != nil {
		check.Status, check.Error = "error", mcpError(err, config)
		if config.OAuth && !mcpclient.OAuthConfigured(a.configDir, config) {
			check.Status = "authorization_required"
		}
	}
	a.mcpMu.Lock()
	index = a.mcpIndexLocked(config.ID)
	if index < 0 || !sameMCPConnection(a.mcpServers[index], config) {
		a.mcpMu.Unlock()
		http.Error(w, "MCP configuration changed during the check; retry", http.StatusConflict)
		return
	}
	if a.mcpChecks == nil {
		a.mcpChecks = map[string]mcpCheck{}
	}
	if r.Context().Err() == nil {
		a.mcpChecks[config.ID] = check
	}
	a.mcpMu.Unlock()
	jsonOut(w, struct {
		Check mcpCheck         `json:"check"`
		Tools []mcpclient.Tool `json:"tools"`
	}{check, tools})
}

func (a *App) checkMCP(ctx context.Context, config workerproto.MCPServer) ([]mcpclient.Tool, error) {
	server, err := mcpclient.OpenWithConfigDir(ctx, config, a.configDir)
	if err != nil {
		return nil, err
	}
	defer server.Close()
	tools := []mcpclient.Tool{}
	cursor := ""
	seen := map[string]bool{}
	for {
		page, err := server.ListTools(ctx, cursor)
		if err != nil {
			return nil, err
		}
		tools = append(tools, page.Tools...)
		if len(tools) > 1024 {
			return nil, errors.New("MCP discovery exceeds 1024 tools")
		}
		if page.NextCursor == "" {
			return tools, nil
		}
		if seen[page.NextCursor] {
			return nil, errors.New("MCP server repeated a tool-list cursor")
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
	}
}

func mcpError(err error, config workerproto.MCPServer) string {
	message := err.Error()
	for _, secret := range []string{config.Token, config.ClientSecret} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	if runes := []rune(message); len(runes) > 2048 {
		message = string(runes[:2048]) + "…"
	}
	return message
}
