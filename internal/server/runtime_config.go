package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"vista/internal/configstore"
)

const DefaultPort = 5186

// RuntimeConfig configures the local HTTP listener independently of model and
// Agent settings. Changes take effect on the next Server start.
type RuntimeConfig struct {
	Port int    `json:"port"`
	Host string `json:"-"` // Startup environment only; the settings form edits the port.
}

func (c RuntimeConfig) Address() string {
	host := c.Host
	if host == "" {
		host = "0.0.0.0"
	}
	return net.JoinHostPort(host, strconv.Itoa(c.Port))
}

// ClientAddress never advertises an unspecified bind address to the CLI.
func (c RuntimeConfig) ClientAddress() string {
	if c.Host == "" || c.Host == "0.0.0.0" {
		c.Host = "127.0.0.1"
	}
	return c.Address()
}

func parseRuntimeConfig(data []byte) (RuntimeConfig, error) {
	config := RuntimeConfig{Port: DefaultPort}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	file := &config
	if err := decoder.Decode(&file); err != nil {
		return RuntimeConfig{}, err
	}
	if file == nil {
		return RuntimeConfig{}, errors.New("expected a JSON object")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return RuntimeConfig{}, errors.New("expected one JSON object")
	}
	return config, nil
}

// LoadRuntimeConfig resolves CLI > environment > file > default. A zero CLI
// override means the command did not specify a port; configured port zero is
// rejected so production startup never requests an ephemeral listener.
func LoadRuntimeConfig(configDir string, portOverride int) (RuntimeConfig, error) {
	data, present, err := configstore.Read(configDir, configstore.Server)
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("read server configuration: %w", err)
	}
	return resolveRuntimeConfig(data, present, portOverride)
}

// PreviewRuntimeConfig is startup-only and read-only. It validates old startup
// inputs before a CLI restart can stop the current process, without migrating.
func PreviewRuntimeConfig(configDir string, portOverride int) (RuntimeConfig, error) {
	data, present, err := configstore.PreviewServer(configDir)
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("read startup configuration: %w", err)
	}
	return resolveRuntimeConfig(data, present, portOverride)
}
func resolveRuntimeConfig(data []byte, present bool, portOverride int) (RuntimeConfig, error) {
	config := RuntimeConfig{Port: DefaultPort}
	var err error
	if present {
		config, err = parseRuntimeConfig(data)
		if err != nil {
			return RuntimeConfig{}, fmt.Errorf("parse config.json server section: %w", err)
		}
	}
	if value := strings.TrimSpace(os.Getenv("VISTA_HOST")); value != "" {
		ip := net.ParseIP(value)
		if ip == nil || ip.To4() == nil {
			return RuntimeConfig{}, errors.New("VISTA_HOST must be an IPv4 address")
		}
		config.Host = ip.To4().String()
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
