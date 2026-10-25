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
			http.Error(w, fmt.Sprintf("could not read global instructions: %v", err), http.StatusInternalServerError)
			return
		}
		settings, err := agentconfig.ReadSettings(a.configDir)
		if err != nil {
			http.Error(w, fmt.Sprintf("could not read agent settings: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		jsonOut(w, map[string]any{"instructions": instructions, "instructionsPath": agentconfig.InstructionsPath(a.configDir), "delegationDepth": settings.DelegationDepth, "maxDelegationDepth": agentconfig.MaxDelegationDepth, "automaticTitles": settings.AutomaticTitles})
	case http.MethodPut:
		var input struct {
			Instructions    *string           `json:"instructions"`
			DelegationDepth presentRawMessage `json:"delegationDepth"`
			AutomaticTitles presentRawMessage `json:"automaticTitles"`
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
		if input.Instructions == nil && !input.DelegationDepth.set && !input.AutomaticTitles.set {
			http.Error(w, "instructions, delegationDepth or automaticTitles are required", http.StatusBadRequest)
			return
		}
		var depth *int
		if input.DelegationDepth.set {
			if err := json.Unmarshal(input.DelegationDepth.value, &depth); err != nil || depth == nil || agentconfig.ValidateDelegationDepth(*depth) != nil {
				http.Error(w, fmt.Sprintf("delegationDepth must be an integer between 0 and %d", agentconfig.MaxDelegationDepth), http.StatusBadRequest)
				return
			}
		}
		var automaticTitles *bool
		if input.AutomaticTitles.set {
			if err := json.Unmarshal(input.AutomaticTitles.value, &automaticTitles); err != nil || automaticTitles == nil {
				http.Error(w, "automaticTitles must be a boolean", http.StatusBadRequest)
				return
			}
		}
		// Partial edits from different clients must retain each other's fields.
		a.agentSettingsMu.Lock()
		defer a.agentSettingsMu.Unlock()
		var settings agentconfig.Settings
		if depth != nil || automaticTitles != nil {
			var err error
			settings, err = agentconfig.ReadSettings(a.configDir)
			if err != nil {
				http.Error(w, fmt.Sprintf("could not read agent settings: %v", err), http.StatusInternalServerError)
				return
			}
			if depth != nil {
				settings.DelegationDepth = *depth
			}
			if automaticTitles != nil {
				settings.AutomaticTitles = *automaticTitles
			}
		}
		if input.Instructions != nil {
			if err := agentconfig.Write(a.configDir, *input.Instructions); err != nil {
				http.Error(w, fmt.Sprintf("could not save global instructions: %v", err), http.StatusInternalServerError)
				return
			}
		}
		if depth != nil || automaticTitles != nil {
			if err := agentconfig.WriteSettings(a.configDir, settings); err != nil {
				http.Error(w, fmt.Sprintf("could not save agent settings: %v", err), http.StatusInternalServerError)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}
