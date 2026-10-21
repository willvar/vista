package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"vista/internal/codexauth"
	"vista/internal/copilotauth"
	"vista/internal/modelcatalog"
)

type providerLogin struct {
	ID              string    `json:"id"`
	VerificationURL string    `json:"verificationUrl"`
	UserCode        string    `json:"userCode"`
	ExpiresAt       time.Time `json:"expiresAt"`
	Status          string    `json:"status"`
	Error           string    `json:"error,omitempty"`
	kind            string
	cancel          context.CancelFunc
	input           modelcatalog.ConnectionInput
	// Authorization and discovery happen outside the login mutex. The returned
	// commit is called only if the login is still pending and uncanceled.
	prepare func(context.Context) (func() error, error)
}

func providerLoginPath(path string) (kind, id, providerID string) {
	parts := strings.Split(strings.TrimPrefix(path, "/api/provider-auth/"), "/")
	if len(parts) == 0 || len(parts) > 2 {
		return
	}
	kind = parts[0]
	if len(parts) == 2 {
		id = parts[1]
	}
	switch kind {
	case "codex":
		providerID = "openai-codex"
	case "copilot":
		providerID = "github-copilot"
	}
	return
}

func (a *App) startProviderLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		settingsHTTPError(w, errors.New("method"), 405)
		return
	}
	kind, _, providerID := providerLoginPath(r.URL.Path)
	if providerID == "" {
		http.NotFound(w, r)
		return
	}
	var input struct {
		Key         string               `json:"key"`
		Name        string               `json:"name"`
		CopilotAuth *copilotauth.Options `json:"copilotAuth"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if decodeSettingsRequest(r.Body, &input) != nil || !modelcatalog.ValidCustomProviderName(input.Name) || len(input.Key) > 64 {
		settingsHTTPError(w, errors.New("invalid provider login request"), 400)
		return
	}
	if input.Key != "" {
		snapshot, err := a.modelSettingsSnapshot()
		if err != nil {
			settingsHTTPError(w, err, 500)
			return
		}
		found := false
		for _, model := range snapshot.raw.Models {
			if model.Key == input.Key && model.ProviderID == providerID && modelcatalog.Editable(model) {
				found = true
				if input.CopilotAuth == nil {
					input.CopilotAuth = model.CopilotAuth
				}
			}
		}
		if !found {
			settingsHTTPError(w, errors.New("provider connection is no longer available"), 400)
			return
		}
	}
	if kind == "copilot" {
		options := copilotauth.Options{}
		if input.CopilotAuth != nil {
			options = *input.CopilotAuth
		}
		options, err := options.Normalize()
		if err != nil {
			settingsHTTPError(w, err, 400)
			return
		}
		input.CopilotAuth = &options
	} else if input.CopilotAuth != nil {
		settingsHTTPError(w, errors.New("Copilot login settings are only supported for GitHub Copilot"), 400)
		return
	}
	a.providerLoginMu.Lock()
	if a.codexAuthClient == nil {
		a.codexAuthClient = codexauth.NewClient()
	}
	if a.copilotAuthClient == nil {
		a.copilotAuthClient = copilotauth.NewClient()
	}
	if a.providerLogins == nil {
		a.providerLogins = map[string]*providerLogin{}
	}
	for id, login := range a.providerLogins {
		if time.Now().After(login.ExpiresAt) || login.Status == "canceled" || login.Status == "failed" || login.Status == "expired" {
			login.cancel()
			delete(a.providerLogins, id)
		}
	}
	codexClient, copilotClient := a.codexAuthClient, a.copilotAuthClient
	tooMany := len(a.providerLogins) >= 16
	a.providerLoginMu.Unlock()
	if tooMany {
		settingsHTTPError(w, errors.New("too many login attempts; cancel an earlier login or wait for it to expire"), 429)
		return
	}
	login := &providerLogin{ID: uuid.NewString(), kind: kind, Status: "pending",
		input: modelcatalog.ConnectionInput{Key: input.Key, Name: input.Name, Provider: "catalog", ProviderID: providerID, CredentialMode: "oauth"}}
	if kind == "codex" {
		device, err := codexClient.Begin(r.Context())
		if err != nil {
			settingsHTTPError(w, err, 502)
			return
		}
		login.VerificationURL, login.UserCode, login.ExpiresAt = device.VerificationURL, device.UserCode, device.ExpiresAt
		login.prepare = func(ctx context.Context) (func() error, error) {
			credential, err := codexClient.Complete(ctx, device)
			return func() error { return a.connectCodexAccount(ctx, login.input, credential) }, err
		}
	} else {
		// Each attempt owns its options; concurrent accounts must never change
		// the shared client or another account's polling endpoints.
		client := *copilotClient
		if input.CopilotAuth.EnterpriseURL != "" {
			client.Issuer = input.CopilotAuth.EnterpriseURL
		}
		client.ClientID = input.CopilotAuth.ClientID
		copilotClient = &client
		login.input.CopilotAuth = input.CopilotAuth
		device, err := copilotClient.Begin(r.Context())
		if err != nil {
			settingsHTTPError(w, err, 502)
			return
		}
		login.VerificationURL, login.UserCode, login.ExpiresAt = device.VerificationURL, device.UserCode, device.ExpiresAt
		login.prepare = func(ctx context.Context) (func() error, error) {
			credential, err := copilotClient.Complete(ctx, device)
			if err != nil {
				return nil, err
			}
			snapshot, err := a.modelSettingsSnapshot()
			if err != nil {
				return nil, err
			}
			input := login.input
			input.APIKey, input.BaseURL = credential.Token, credential.APIEndpoint
			method := http.MethodPost
			if input.Key != "" {
				method = http.MethodPut
			}
			raw, err := modelcatalog.ChangeConnection(ctx, snapshot.raw, snapshot.exists, method, input)
			return func() error { return a.commitModelSettings(ctx, snapshot, raw) }, err
		}
	}
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithDeadline(parent, login.ExpiresAt)
	login.cancel = cancel
	a.providerLoginMu.Lock()
	defer a.providerLoginMu.Unlock()
	if r.Context().Err() != nil || len(a.providerLogins) >= 16 {
		cancel()
		settingsHTTPError(w, errors.New("provider login could not start; retry"), http.StatusTooManyRequests)
		return
	}
	a.providerLogins[login.ID] = login
	go a.completeProviderLogin(ctx, login)
	jsonOut(w, login)
}

func (a *App) providerLoginStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		settingsHTTPError(w, errors.New("method"), 405)
		return
	}
	kind, id, _ := providerLoginPath(r.URL.Path)
	a.providerLoginMu.Lock()
	defer a.providerLoginMu.Unlock()
	login := a.providerLogins[id]
	if login == nil || login.kind != kind {
		settingsHTTPError(w, errors.New("provider login expired; start login again"), 404)
		return
	}
	if r.Method == http.MethodDelete {
		login.cancel()
		if login.Status == "pending" {
			login.Status = "canceled"
		}
	}
	jsonOut(w, login)
}

func (a *App) completeProviderLogin(ctx context.Context, login *providerLogin) {
	defer login.cancel()
	commit, err := login.prepare(ctx)
	a.providerLoginMu.Lock()
	defer a.providerLoginMu.Unlock()
	if login.Status != "pending" {
		return
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil {
		err = commit()
	}
	if err != nil {
		login.Status, login.Error = "failed", err.Error()
		if errors.Is(err, context.DeadlineExceeded) {
			login.Status, login.Error = "expired", "provider login expired; start login again"
		}
		if errors.Is(err, context.Canceled) {
			login.Status, login.Error = "canceled", ""
		}
		return
	}
	login.Status = "connected"
}

func (a *App) connectCodexAccount(ctx context.Context, input modelcatalog.ConnectionInput, credential codexauth.Credential) error {
	snapshot, err := a.modelSettingsSnapshot()
	if err != nil {
		return err
	}
	method := http.MethodPost
	if input.Key != "" {
		method = http.MethodPut
	}
	raw, err := modelcatalog.ChangeConnection(ctx, snapshot.raw, snapshot.exists, method, input)
	if err != nil {
		return err
	}
	connectionID := ""
	if input.Key != "" {
		for _, model := range raw.Models {
			if model.Key == input.Key {
				connectionID = model.ConnectionID
			}
		}
	} else {
		connectionID = raw.Models[len(raw.Models)-1].ConnectionID
	}
	path := codexauth.Path(a.configDir, connectionID)
	a.modelMu.Lock()
	defer a.modelMu.Unlock()
	if a.modelRevision != snapshot.revision {
		return errors.New("provider settings changed during login; start login again")
	}
	previous, previousErr := codexauth.Read(ctx, path)
	if err := codexauth.Save(ctx, path, credential); err != nil {
		return errors.New("could not save Codex account")
	}
	if err := a.commitModelSettingsLocked(ctx, snapshot, raw); err != nil {
		rollback, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if previousErr == nil {
			_ = codexauth.Save(rollback, path, previous)
		} else {
			_ = codexauth.Remove(rollback, path)
		}
		return err
	}
	return nil
}
