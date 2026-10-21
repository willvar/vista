package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"vista/internal/agentconfig"
)

type presentRawMessage struct {
	value json.RawMessage
	set   bool
}

func (m *presentRawMessage) UnmarshalJSON(data []byte) error {
	m.value = append(m.value[:0], data...)
	m.set = true
	return nil
}

func (a *App) agentSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		instructions, err := agentconfig.Read(a.configDir)
		if err != nil {
			http.Error(w, "could not read agent settings", http.StatusInternalServerError)
			return
		}
		settings, err := agentconfig.ReadSettings(a.configDir)
		if err != nil {
			http.Error(w, "could not read agent settings", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		jsonOut(w, map[string]any{"instructions": instructions, "instructionsPath": agentconfig.InstructionsPath(a.configDir), "maxBytes": agentconfig.MaxInstructionsBytes, "delegationDepth": settings.DelegationDepth, "maxDelegationDepth": agentconfig.MaxDelegationDepth})
	case http.MethodPut:
		r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		var input struct {
			Instructions    *string           `json:"instructions"`
			DelegationDepth presentRawMessage `json:"delegationDepth"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			http.Error(w, "invalid agent settings request", http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			http.Error(w, "invalid agent settings request", http.StatusBadRequest)
			return
		}
		if input.Instructions == nil && !input.DelegationDepth.set {
			http.Error(w, "instructions or delegationDepth are required", http.StatusBadRequest)
			return
		}
		var settings agentconfig.Settings
		if input.DelegationDepth.set {
			var depth *int
			if err := json.Unmarshal(input.DelegationDepth.value, &depth); err != nil || depth == nil || agentconfig.ValidateDelegationDepth(*depth) != nil {
				http.Error(w, fmt.Sprintf("delegationDepth must be an integer between 0 and %d", agentconfig.MaxDelegationDepth), http.StatusBadRequest)
				return
			}
			settings.DelegationDepth = *depth
		}
		if input.Instructions != nil && len(*input.Instructions) > agentconfig.MaxInstructionsBytes {
			http.Error(w, "agent instructions exceed 4096 bytes", http.StatusBadRequest)
			return
		}
		if input.Instructions != nil {
			if err := agentconfig.Write(a.configDir, *input.Instructions); err != nil {
				http.Error(w, "could not save agent settings", http.StatusInternalServerError)
				return
			}
		}
		if input.DelegationDepth.set {
			if err := agentconfig.WriteSettings(a.configDir, settings); err != nil {
				http.Error(w, "could not save agent settings", http.StatusInternalServerError)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}
