package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"vista/internal/configstore"
	"vista/internal/network"
	"vista/internal/workerproto"
)

type proxyProfileView struct {
	network.Profile
	HasCredentials bool `json:"hasCredentials"`
}
type networkConfigView struct {
	Config struct {
		GlobalProxy string             `json:"globalProxy"`
		Profiles    []proxyProfileView `json:"profiles"`
	} `json:"config"`
	Revision string `json:"revision"`
	Version  string `json:"version"`
}
type networkConfigInput struct {
	Config           network.Settings `json:"config"`
	Revision         string           `json:"revision"`
	ReuseCredentials []string         `json:"reuseCredentials,omitempty"`
	ProfileID        string           `json:"profileId,omitempty"`
	Target           string           `json:"target,omitempty"`
}
type networkReference struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Key  string `json:"key,omitempty"`
}

func (a *App) globalNetworkPolicy() (network.Policy, error) {
	s, e := network.Load(a.configDir)
	if e != nil {
		return network.Policy{}, e
	}
	return s.Select(s.GlobalProxy)
}

// SSE supplies its checkpoint version; HTTP captures one before reading so an
// older configuration cannot acknowledge a commit that finishes during the read.
func (a *App) networkConfigSnapshot(version string) (networkConfigView, error) {
	if version == "" {
		version = a.stateVersion("network")
	}
	data, present, err := configstore.Read(a.configDir, configstore.Network)
	if err != nil {
		return networkConfigView{}, err
	}
	settings, err := network.Decode(data, present)
	if err != nil {
		return networkConfigView{}, err
	}
	var view networkConfigView
	view.Revision = a.networkRevision(data, present)
	view.Version = version
	view.Config.GlobalProxy = settings.GlobalProxy
	view.Config.Profiles = []proxyProfileView{}
	for _, p := range settings.Profiles {
		has := p.Username != "" || p.Password != ""
		p.Username = ""
		p.Password = ""
		view.Config.Profiles = append(view.Config.Profiles, proxyProfileView{p, has})
	}
	return view, nil
}
func mergeNetworkCredentials(candidate network.Settings, previous network.Settings, reuse []string) (network.Settings, error) {
	allowed := map[string]bool{}
	for _, id := range reuse {
		allowed[id] = true
	}
	for index, p := range candidate.Profiles {
		if p.ID == "" {
			p.ID = uuid.NewString()
		}
		p.Name = strings.TrimSpace(p.Name)
		p.Server = strings.TrimSpace(p.Server)
		p.ConnectIP = strings.TrimSpace(p.ConnectIP)
		if p.Authentication == "none" {
			p.Username = ""
			p.Password = ""
		} else if p.Username == "" && p.Password == "" {
			for _, old := range previous.Profiles {
				if old.ID == p.ID {
					if old.Protocol != p.Protocol || old.Server != p.Server || old.Port != p.Port || old.ConnectIP != p.ConnectIP {
						if !allowed[p.ID] {
							return candidate, errors.New("confirm credential reuse or enter new credentials for the changed proxy entry")
						}
					}
					p.Username, p.Password = old.Username, old.Password
					break
				}
			}
		}
		candidate.Profiles[index] = p
	}
	return candidate, candidate.Validate()
}
func (a *App) networkConfigHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		view, err := a.networkConfigSnapshot("")
		if err != nil {
			settingsHTTPError(w, err, 500)
			return
		}
		jsonOut(w, view)
		return
	}
	if r.Method != http.MethodPut {
		settingsHTTPError(w, errors.New("method"), 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	var input networkConfigInput
	if err := decodeSettingsRequest(r.Body, &input); err != nil {
		settingsHTTPError(w, errors.New("invalid network settings request"), 400)
		return
	}
	// Provider binding commits use the same lock, so delete/reference validation
	// cannot race a Provider save between inspection and section publication.
	a.modelMu.Lock()
	var references []networkReference
	globalChanged := false
	err := configstore.Update(a.configDir, configstore.Network, func(data []byte, present bool) ([]byte, error) {
		if a.networkRevision(data, present) != input.Revision {
			return nil, configstore.ErrConflict
		}
		previous, err := network.Decode(data, present)
		if err != nil {
			return nil, err
		}
		exists := map[string]bool{}
		for _, p := range input.Config.Profiles {
			exists[p.ID] = true
		}
		if input.Config.GlobalProxy != "" && !exists[input.Config.GlobalProxy] {
			references = append(references, networkReference{Kind: "global", Name: "Vista"})
		}
		seen := map[string]bool{}
		var stored struct {
			Models []struct {
				Key          string `json:"key"`
				ConnectionID string `json:"connectionId"`
				Name         string `json:"name"`
				ProxyID      string `json:"proxyId"`
			} `json:"models"`
		}
		modelData, modelsPresent, err := configstore.Read(a.configDir, configstore.Models)
		if err != nil {
			return nil, err
		}
		if modelsPresent && json.Unmarshal(modelData, &stored) != nil {
			return nil, errors.New("invalid model references")
		}
		for _, model := range stored.Models {
			key := model.ConnectionID
			if key == "" {
				key = model.Key
			}
			if model.ProxyID != "" && !exists[model.ProxyID] && !seen[key] {
				references = append(references, networkReference{Kind: "provider", Name: model.Name, Key: model.Key})
				seen[key] = true
			}
		}
		if len(references) > 0 {
			return nil, errors.New("proxy profile is still used")
		}
		candidate, err := mergeNetworkCredentials(input.Config, previous, input.ReuseCredentials)
		if err != nil {
			return nil, err
		}
		before, _ := previous.Select(previous.GlobalProxy)
		after, _ := candidate.Select(candidate.GlobalProxy)
		globalChanged = network.Fingerprint(before) != network.Fingerprint(after)
		return json.Marshal(candidate)
	})
	a.modelMu.Unlock()
	if err != nil {
		if len(references) > 0 {
			w.WriteHeader(http.StatusConflict)
			jsonOut(w, map[string]any{"code": "proxy_in_use", "message": "Remove the proxy selection at these locations before deleting it.", "details": map[string]any{"references": references}})
			return
		}
		status := http.StatusBadRequest
		if errors.Is(err, configstore.ErrConflict) {
			status = http.StatusConflict
		}
		settingsHTTPError(w, err, status)
		return
	}
	a.updates.notify("network")
	if globalChanged {
		a.mcpMu.Lock()
		for _, server := range a.mcpServers {
			if server.Transport == "http" {
				delete(a.mcpChecks, server.ID)
			}
		}
		a.mcpMu.Unlock()
		a.updates.notify("mcp")
		if a.mcpHost != nil {
			a.mcpHost.Refresh()
		}
	}
	view, err := a.networkConfigSnapshot("")
	if err != nil {
		settingsHTTPError(w, err, 500)
		return
	}
	jsonOut(w, view)
}
func (a *App) networkRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := network.WithOperation(network.WithConfigDir(r.Context(), a.configDir))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
func (a *App) networkCheckHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		settingsHTTPError(w, errors.New("method"), 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	var input networkConfigInput
	if decodeSettingsRequest(r.Body, &input) != nil {
		settingsHTTPError(w, errors.New("invalid network check request"), 400)
		return
	}
	data, present, err := configstore.Read(a.configDir, configstore.Network)
	if err != nil {
		settingsHTTPError(w, err, 500)
		return
	}
	if a.networkRevision(data, present) != input.Revision {
		settingsHTTPError(w, configstore.ErrConflict, 409)
		return
	}
	previous, err := network.Decode(data, present)
	if err != nil {
		settingsHTTPError(w, err, 500)
		return
	}
	candidate, err := mergeNetworkCredentials(input.Config, previous, input.ReuseCredentials)
	if err != nil {
		settingsHTTPError(w, err, 400)
		return
	}
	policy, err := candidate.Select(input.ProfileID)
	if err != nil {
		settingsHTTPError(w, err, 400)
		return
	}
	target, err := url.Parse(input.Target)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" || target.User != nil || target.Fragment != "" || target.RawQuery != "" {
		settingsHTTPError(w, errors.New("check target must be an HTTP or HTTPS URL without credentials, query or fragment"), 400)
		return
	}
	ctx, cancel := context.WithTimeout(network.WithPolicy(r.Context(), policy), 25*time.Second)
	defer cancel()
	stage, status := "request", 0
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	client := network.NewClient(http.Client{Transport: network.Transport{Policy: &policy}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, network.ApplicationScope)
	res, err := client.Do(req)
	if res != nil {
		status = res.StatusCode
		res.Body.Close()
	}
	message := ""
	if err != nil {
		message = network.Sanitize(err, policy).Error()
		stage = network.FailureStage(err)
	}
	route := "direct"
	if selected := policy.Effective(target); selected.Profile != nil {
		route = selected.Profile.Name
	}
	jsonOut(w, map[string]any{"route": route, "ok": err == nil, "stage": stage, "httpStatus": status, "error": message, "checkedAt": time.Now().UTC(), "target": target.String()})
}

func pinMCPNetwork(policy network.Policy, servers []workerproto.MCPServer, searches []workerproto.SearchProvider) {
	for index := range servers {
		if servers[index].Transport == "http" {
			p := policy
			servers[index].Network = &p
		}
	}
	for index := range searches {
		p := policy
		searches[index].Network = &p
	}
}

var networkRevisionKey = []byte(rand.Text())

func (a *App) networkRevision(data []byte, present bool) string {
	mac := hmac.New(sha256.New, networkRevisionKey)
	mac.Write([]byte(a.updates.snapshot().Epoch))
	mac.Write([]byte{0})
	mac.Write([]byte(configstore.Revision(data, present)))
	return hex.EncodeToString(mac.Sum(nil))
}
