package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"vista/internal/mcpclient"
	"vista/internal/workerproto"
)

type mcpLogin struct {
	ID               string    `json:"id"`
	ServerID         string    `json:"serverId"`
	Name             string    `json:"name"`
	Status           string    `json:"status"`
	AuthorizationURL string    `json:"authorizationUrl,omitempty"`
	ExpiresAt        time.Time `json:"expiresAt"`
	Error            string    `json:"error,omitempty"`
	cancel           context.CancelFunc
	callback         chan string
}

func (l *mcpLogin) active() bool {
	return l.Status == "starting" || l.Status == "pending" || l.Status == "exchanging"
}

type mcpStateSnapshot struct {
	mcpConfigSnapshot
	Logins []loginUpdate `json:"logins"`
}

func (a *App) mcpStateSnapshot() mcpStateSnapshot {
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()
	items := make([]loginUpdate, 0, len(a.mcpLogins))
	for _, login := range a.mcpLogins {
		items = append(items, loginUpdate{login.ID, login.Status, login.Error})
	}
	return mcpStateSnapshot{a.mcpConfigSnapshotLocked(), items}
}

func (a *App) cancelMCPLoginLocked(id string) {
	defer a.updates.notify("mcp")
	for _, login := range a.mcpLogins {
		if login.ServerID == id && login.active() {
			login.Status, login.AuthorizationURL = "canceled", ""
			login.cancel()
		}
	}
}

func (a *App) startMCPLoginLocked(w http.ResponseWriter, config workerproto.MCPServer) {
	if !config.OAuth || config.Transport != "http" {
		http.Error(w, "this MCP server does not use OAuth", http.StatusBadRequest)
		return
	}
	staging, err := os.MkdirTemp(a.configDir, ".mcp-login-*")
	if err != nil {
		http.Error(w, "could not prepare OAuth login", http.StatusInternalServerError)
		return
	}
	a.cancelMCPLoginLocked(config.ID)
	if a.mcpLogins == nil {
		a.mcpLogins = map[string]*mcpLogin{}
	}
	// Keep recent terminal results readable by other browser tabs. Bound the
	// cache as well as the one-active-attempt-per-server rule.
	for id, login := range a.mcpLogins {
		if !login.active() && time.Now().After(login.ExpiresAt) {
			delete(a.mcpLogins, id)
		}
	}
	if len(a.mcpLogins) >= 16 {
		var oldest *mcpLogin
		for _, login := range a.mcpLogins {
			if !login.active() && (oldest == nil || login.ExpiresAt.Before(oldest.ExpiresAt)) {
				oldest = login
			}
		}
		if oldest != nil {
			delete(a.mcpLogins, oldest.ID)
		}
	}
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	expires := time.Now().UTC().Add(5 * time.Minute)
	ctx, cancel := context.WithDeadline(parent, expires)
	login := &mcpLogin{ID: uuid.NewString(), ServerID: config.ID, Name: config.Name, Status: "starting", ExpiresAt: expires, cancel: cancel, callback: make(chan string, 1)}
	a.mcpLogins[login.ID] = login
	a.background.Go(func() { a.runMCPLogin(ctx, login, config, staging) })
	jsonOut(w, login)
}

func (a *App) runMCPLogin(ctx context.Context, login *mcpLogin, config workerproto.MCPServer, staging string) {
	defer a.updates.notify("mcp")
	defer os.RemoveAll(staging)
	defer login.cancel()
	err := mcpclient.Login(ctx, config, staging, func(authCtx context.Context, raw string) (string, error) {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return "", errors.New("invalid MCP authorization URL")
		}
		a.mcpMu.Lock()
		if !login.active() || ctx.Err() != nil || authCtx.Err() != nil {
			a.mcpMu.Unlock()
			return "", context.Canceled
		}
		login.Status, login.AuthorizationURL = "pending", raw
		a.updates.notify("mcp")
		a.mcpMu.Unlock()
		select {
		case callback := <-login.callback:
			return callback, nil
		case <-authCtx.Done():
			return "", authCtx.Err()
		}
	})
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()
	if !login.active() {
		return
	}
	login.AuthorizationURL = ""
	index := a.mcpIndexLocked(config.ID)
	if index < 0 || !sameMCPConnection(a.mcpServers[index], config) {
		login.Status = "canceled"
		return
	}
	if err == nil {
		err = mcpclient.CommitOAuth(ctx, staging, a.configDir, config)
	}
	if ctx.Err() != nil {
		login.Status = "canceled"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			login.Status = "expired"
		}
		return
	}
	if err != nil {
		login.Status, login.Error = "failed", "MCP OAuth authorization or connection setup failed; verify the service and start again"
		return
	}
	login.Status = "connected"
	delete(a.mcpChecks, config.ID)
	a.refreshMCPHost()
}

func (a *App) mcpLoginStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodDelete && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		CallbackURL string `json:"callbackUrl"`
	}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := decodeSettingsRequest(r.Body, &input); err != nil {
			http.Error(w, "invalid OAuth callback request", http.StatusBadRequest)
			return
		}
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/mcp-auth/")
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()
	login := a.mcpLogins[id]
	if login == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		if !login.active() || !time.Now().Before(login.ExpiresAt) {
			if login.active() {
				login.Status, login.AuthorizationURL = "expired", ""
				login.cancel()
				a.updates.notify("mcp")
			}
			http.Error(w, "OAuth login has ended; start again", http.StatusConflict)
			return
		}
		if login.Status != "pending" || login.AuthorizationURL == "" {
			http.Error(w, "OAuth login is not waiting for a callback", http.StatusConflict)
			return
		}
		if err := mcpclient.ValidateOAuthCallback(login.AuthorizationURL, input.CallbackURL); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		login.callback <- strings.TrimSpace(input.CallbackURL)
		login.Status, login.AuthorizationURL = "exchanging", ""
		a.updates.notify("mcp")
	}
	if r.Method == http.MethodDelete && login.active() {
		login.Status, login.AuthorizationURL = "canceled", ""
		login.cancel()
	}
	jsonOut(w, login)
}
