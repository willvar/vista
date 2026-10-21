package agentconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const DefaultDelegationDepth = 1
const MaxDelegationDepth = 8

const settingsFilename = "agent-settings.json"

// DelegationDepth counts the levels below the main Agent: 0 disables
// delegation, 1 allows direct subagents, and 2 also allows their children.
type Settings struct {
	DelegationDepth int `json:"delegationDepth"`
}

func ValidateDelegationDepth(depth int) error {
	if depth < 0 || depth > MaxDelegationDepth {
		return fmt.Errorf("delegationDepth must be an integer between 0 and %d", MaxDelegationDepth)
	}
	return nil
}

func ReadSettings(dir string) (Settings, error) {
	settings := Settings{DelegationDepth: DefaultDelegationDepth}
	file, err := os.Open(filepath.Join(dir, settingsFilename))
	if errors.Is(err, os.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return Settings{}, err
	}
	defer file.Close()
	var input struct {
		DelegationDepth *int `json:"delegationDepth"`
	}
	decoder := json.NewDecoder(io.LimitReader(file, 4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return Settings{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Settings{}, errors.New("invalid agent settings file")
	}
	if input.DelegationDepth == nil {
		return Settings{}, errors.New("delegationDepth is required")
	}
	settings = Settings{DelegationDepth: *input.DelegationDepth}
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
	return writePrivateFile(dir, settingsFilename, append(data, '\n'), false)
}
