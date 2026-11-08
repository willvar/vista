package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"

	"vista/internal/configstore"
)

const serverConfigFilename = configstore.Filename
const missingServerConfigRevision = "missing"
const maxServerConfigBytes = 4 << 10

var errServerConfigChanged = errors.New("server configuration changed; read it again before saving")
var errInvalidServerConfig = errors.New("server configuration must contain only an integer port from 1 to 65535")

type serverConfigState struct {
	Config      RuntimeConfig  `json:"config"`
	Revision    string         `json:"revision"`
	Version     string         `json:"version"`
	Source      string         `json:"source"`
	RunningPort int            `json:"runningPort"`
	Schema      map[string]any `json:"schema"`
}

func newServerConfigState(config RuntimeConfig, revision, version string, runningPort int) serverConfigState {
	source := "file"
	if revision == missingServerConfigRevision {
		source = "default"
	}
	return serverConfigState{
		Config: config, Revision: revision, Version: version, Source: source, RunningPort: runningPort,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{"port": map[string]any{
				"type": "integer", "minimum": 1, "maximum": 65535, "default": DefaultPort,
			}},
			"required": []string{"port"}, "additionalProperties": false,
		},
	}
}

// This small, fixed settings object rejects duplicate and case-variant keys as
// well as trailing data. It is not a general configuration-file editor.
func serverConfigObject(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errInvalidServerConfig
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || fields[name] != nil {
			return nil, errInvalidServerConfig
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, errInvalidServerConfig
		}
		fields[name] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') || decoder.Decode(new(any)) != io.EOF {
		return nil, errInvalidServerConfig
	}
	return fields, nil
}

func parseServerConfig(data []byte, requirePort bool) (RuntimeConfig, error) {
	fields, err := serverConfigObject(data)
	if err != nil || len(fields) > 1 || len(fields) == 1 && fields["port"] == nil || requirePort && fields["port"] == nil {
		return RuntimeConfig{}, errInvalidServerConfig
	}
	if value := fields["port"]; value != nil && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return RuntimeConfig{}, errInvalidServerConfig
	}
	config, err := parseRuntimeConfig(data)
	if err != nil || config.Port < 1 || config.Port > 65535 {
		return RuntimeConfig{}, errInvalidServerConfig
	}
	return config, nil
}

func parseServerConfigRequest(data []byte) (RuntimeConfig, string, bool, error) {
	fields, err := serverConfigObject(data)
	if err != nil || len(fields) < 2 || len(fields) > 3 || fields["config"] == nil || fields["revision"] == nil || len(fields) == 3 && fields["confirmRestart"] == nil {
		return RuntimeConfig{}, "", false, errInvalidServerConfig
	}
	var revision *string
	if json.Unmarshal(fields["revision"], &revision) != nil || revision == nil || *revision == "" || len(*revision) > sha256.Size*2 {
		return RuntimeConfig{}, "", false, errInvalidServerConfig
	}
	confirmed := false
	if value := fields["confirmRestart"]; value != nil {
		if json.Unmarshal(value, &confirmed) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return RuntimeConfig{}, "", false, errInvalidServerConfig
		}
	}
	config, err := parseServerConfig(fields["config"], true)
	return config, *revision, confirmed, err
}

func serverConfigRunningPort(host string) (int, error) {
	_, value, err := net.SplitHostPort(host)
	if err != nil {
		return 0, errors.New("invalid listener address")
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("invalid listener address")
	}
	return port, nil
}

func (a *App) actualServerPort(host string) (int, error) {
	if a.listenerPort != nil {
		return a.listenerPort(), nil
	}
	return serverConfigRunningPort(host)
}

func serverConfigDigest(data []byte) string { return configstore.Revision(data, true) }

// The supplied version is captured before the reads for shared SSE snapshots;
// HTTP reads capture their own version while holding the settings mutex.
func (a *App) serverConfigSnapshot(host, version string) (serverConfigState, error) {
	runningPort, err := a.actualServerPort(host)
	if err != nil {
		return serverConfigState{}, err
	}
	a.serverConfigMu.Lock()
	defer a.serverConfigMu.Unlock()
	if version == "" {
		version = a.stateVersion("server-config")
	}
	data, present, err := configstore.Read(a.configDir, configstore.Server)
	if err != nil {
		return serverConfigState{}, err
	}
	revision := configstore.Revision(data, present)
	config := RuntimeConfig{Port: DefaultPort}
	if present {
		config, err = parseServerConfig(data, false)
		if err != nil {
			return serverConfigState{}, err
		}
	}
	return newServerConfigState(config, revision, version, runningPort), nil
}

// The shared store compares only this section while merging the latest other
// sections inside its cross-process read/modify/write transaction.
func writeServerConfig(dir string, config RuntimeConfig, revision string) ([]byte, error) {
	data, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	if err := configstore.CompareAndSwap(dir, configstore.Server, revision, data); err != nil {
		return nil, err
	}
	return data, nil
}

func (a *App) serverConfigHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		snapshot, err := a.serverConfigSnapshot(r.Host, "")
		if err != nil {
			http.Error(w, "could not read server configuration", http.StatusInternalServerError)
			return
		}
		jsonOut(w, snapshot)
	case http.MethodPut:
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxServerConfigBytes))
		if err != nil {
			status := http.StatusBadRequest
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				status = http.StatusRequestEntityTooLarge
			}
			http.Error(w, "invalid server configuration request", status)
			return
		}
		config, revision, confirmed, err := parseServerConfigRequest(data)
		if err != nil {
			http.Error(w, "config.port and revision are required; port must be an integer from 1 to 65535 with no extra fields", http.StatusBadRequest)
			return
		}
		runningPort, err := a.actualServerPort(r.Host)
		if err != nil {
			http.Error(w, "invalid listener address", http.StatusBadRequest)
			return
		}
		if config.Port != runningPort {
			if a.portChange == nil {
				portConfigError(w, http.StatusServiceUnavailable, "port_restart_unavailable", "Port-change restart is unavailable")
				return
			}
			a.portChange(w, r, config, revision, confirmed)
			return
		}
		a.serverConfigMu.Lock()
		defer a.serverConfigMu.Unlock()
		saved, err := writeServerConfig(a.configDir, config, revision)
		if err != nil {
			if errors.Is(err, configstore.ErrConflict) {
				http.Error(w, errServerConfigChanged.Error(), http.StatusConflict)
			} else {
				http.Error(w, "could not save server configuration", http.StatusInternalServerError)
			}
			return
		}
		// Saving never reloads the listener or changes WebUI restart semantics.
		a.updates.notify("server-config")
		jsonOut(w, newServerConfigState(config, serverConfigDigest(saved), a.stateVersion("server-config"), runningPort))
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}
