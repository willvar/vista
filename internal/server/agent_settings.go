package server

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"

	"vista/internal/agentconfig"
	"vista/internal/tools"
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
		instructions, err := agentconfig.ReadSnapshot(a.configDir)
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
		jsonOut(w, map[string]any{"instructions": instructions.Content, "instructionsSha256": instructions.SHA256, "instructionsPath": agentconfig.InstructionsPath(a.configDir), "delegationDepth": settings.DelegationDepth, "maxDelegationDepth": agentconfig.MaxDelegationDepth, "automaticTitles": settings.AutomaticTitles})
	case http.MethodPut:
		var input struct {
			Instructions    *string           `json:"instructions"`
			ExpectedSHA256  string            `json:"expectedSha256"`
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
		if input.Instructions != nil && input.ExpectedSHA256 == "" || input.Instructions == nil && input.ExpectedSHA256 != "" {
			http.Error(w, "instruction writes require expectedSha256 from GET /api/agent-settings", http.StatusBadRequest)
			return
		}
		if input.Instructions != nil && input.ExpectedSHA256 != "missing" {
			if decoded, err := hex.DecodeString(input.ExpectedSHA256); err != nil || len(decoded) != 32 {
				http.Error(w, "expectedSha256 must be a SHA-256 digest or missing", http.StatusBadRequest)
				return
			}
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
		// Serialize instruction edits locally; section edits also hold the shared store lock.
		a.agentSettingsMu.Lock()
		defer a.agentSettingsMu.Unlock()
		if depth != nil || automaticTitles != nil {
			if _, err := agentconfig.ReadSettings(a.configDir); err != nil {
				http.Error(w, fmt.Sprintf("could not read agent settings: %v", err), http.StatusInternalServerError)
				return
			}
		}
		var instructionsSHA256 string
		if input.Instructions != nil {
			stateDir := a.dir
			if stateDir == "" {
				stateDir = filepath.Join(a.configDir, "state")
			}
			result, err := agentconfig.WriteChecked(r.Context(), stateDir, a.configDir, input.ExpectedSHA256, *input.Instructions)
			if err != nil {
				if errors.Is(err, tools.ErrConflict) {
					http.Error(w, "global instructions changed; reload before saving", http.StatusConflict)
				} else {
					http.Error(w, fmt.Sprintf("could not save global instructions: %v", err), http.StatusInternalServerError)
				}
				return
			}
			instructionsSHA256 = result.SHA256
		}
		if depth != nil || automaticTitles != nil {
			if err := agentconfig.UpdateSettings(a.configDir, depth, automaticTitles); err != nil {
				http.Error(w, fmt.Sprintf("could not save agent settings: %v", err), http.StatusInternalServerError)
				return
			}
		}
		if input.Instructions != nil {
			jsonOut(w, map[string]string{"instructionsSha256": instructionsSHA256})
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}
