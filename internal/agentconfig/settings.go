package agentconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"vista/internal/configstore"
)

const DefaultDelegationDepth = 1
const MaxDelegationDepth = 8

// DelegationDepth counts the levels below the main Agent: 0 disables
// delegation, 1 allows direct subagents, and 2 also allows their children.
type Settings struct {
	DelegationDepth int  `json:"delegationDepth"`
	AutomaticTitles bool `json:"automaticTitles"`
}

func ValidateDelegationDepth(depth int) error {
	if depth < 0 || depth > MaxDelegationDepth {
		return fmt.Errorf("delegationDepth must be an integer between 0 and %d", MaxDelegationDepth)
	}
	return nil
}

func ReadSettings(dir string) (Settings, error) {
	data, exists, err := configstore.Read(dir, configstore.Agent)
	if err != nil {
		return Settings{}, err
	}
	return decodeSettings(data, exists)
}

func decodeSettings(data []byte, exists bool) (Settings, error) {
	settings := Settings{DelegationDepth: DefaultDelegationDepth, AutomaticTitles: true}
	if !exists {
		return settings, nil
	}
	var input struct {
		DelegationDepth *int            `json:"delegationDepth"`
		AutomaticTitles json.RawMessage `json:"automaticTitles"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return Settings{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Settings{}, errors.New("invalid agent settings")
	}
	if input.DelegationDepth == nil {
		return Settings{}, errors.New("delegationDepth is required")
	}
	settings.DelegationDepth = *input.DelegationDepth
	if len(input.AutomaticTitles) > 0 {
		var enabled *bool
		if err := json.Unmarshal(input.AutomaticTitles, &enabled); err != nil || enabled == nil {
			return Settings{}, errors.New("automaticTitles must be a boolean")
		}
		settings.AutomaticTitles = *enabled
	}
	if err := ValidateDelegationDepth(settings.DelegationDepth); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

func WriteSettings(dir string, settings Settings) error {
	if err := ValidateDelegationDepth(settings.DelegationDepth); err != nil {
		return err
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return configstore.Write(dir, configstore.Agent, data)
}

// UpdateSettings applies only supplied fields under the shared configuration lock.
// An omitted automaticTitles field stays omitted rather than becoming explicit.
func UpdateSettings(dir string, delegationDepth *int, automaticTitles *bool) error {
	if delegationDepth == nil && automaticTitles == nil {
		return nil
	}
	if delegationDepth != nil {
		if err := ValidateDelegationDepth(*delegationDepth); err != nil {
			return err
		}
	}
	return configstore.Update(dir, configstore.Agent, func(data []byte, exists bool) ([]byte, error) {
		if _, err := decodeSettings(data, exists); err != nil {
			return nil, err
		}
		var saved struct {
			DelegationDepth int   `json:"delegationDepth"`
			AutomaticTitles *bool `json:"automaticTitles,omitempty"`
		}
		saved.DelegationDepth = DefaultDelegationDepth
		if exists {
			if err := json.Unmarshal(data, &saved); err != nil {
				return nil, err
			}
		}
		if delegationDepth != nil {
			saved.DelegationDepth = *delegationDepth
		}
		if automaticTitles != nil {
			saved.AutomaticTitles = automaticTitles
		}
		return json.Marshal(saved)
	})
}
