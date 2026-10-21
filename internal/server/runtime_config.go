package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const DefaultPort = 5186

// RuntimeConfig configures the local HTTP listener independently of model and
// Agent settings. Changes take effect on the next Server start.
type RuntimeConfig struct {
	Port int `json:"port"`
}

func (c RuntimeConfig) Address() string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Port))
}

// LoadRuntimeConfig resolves CLI > environment > file > default. A zero CLI
// override means the command did not specify a port; configured port zero is
// rejected so production startup never requests an ephemeral listener.
func LoadRuntimeConfig(configDir string, portOverride int) (RuntimeConfig, error) {
	config := RuntimeConfig{Port: DefaultPort}
	path := filepath.Join(configDir, "server.json")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return RuntimeConfig{}, fmt.Errorf("read server configuration: %w", err)
	}
	if err == nil {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		file := &config
		if err := decoder.Decode(&file); err != nil {
			return RuntimeConfig{}, fmt.Errorf("parse %s: %w", path, err)
		}
		if file == nil {
			return RuntimeConfig{}, fmt.Errorf("parse %s: expected a JSON object", path)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return RuntimeConfig{}, fmt.Errorf("parse %s: expected one JSON object", path)
		}
	}
	if value := strings.TrimSpace(os.Getenv("VISTA_PORT")); value != "" && portOverride == 0 {
		port, err := strconv.Atoi(value)
		if err != nil {
			return RuntimeConfig{}, errors.New("VISTA_PORT must be an integer from 1 to 65535")
		}
		config.Port = port
	}
	if portOverride != 0 {
		config.Port = portOverride
	}
	if config.Port < 1 || config.Port > 65535 {
		return RuntimeConfig{}, errors.New("server port must be from 1 to 65535")
	}
	return config, nil
}
