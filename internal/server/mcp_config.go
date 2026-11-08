package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"vista/internal/configstore"
	"vista/internal/mcpclient"
	"vista/internal/workerproto"
)

func loadMCPConfig(dir string) ([]workerproto.MCPServer, error) {
	data, exists, err := configstore.Read(dir, configstore.MCP)
	if err != nil || !exists {
		return nil, err
	}
	if len(data) > 8<<10 {
		return nil, errors.New("mcp section in config.json exceeds 8 KiB")
	}
	var configs []workerproto.MCPServer
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configs); err != nil {
		return nil, fmt.Errorf("parse mcp section in config.json: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("mcp section in config.json has extra data")
	}
	if err := validateMCPConfig(configs); err != nil {
		return nil, err
	}
	return configs, nil
}

func validateMCPConfig(configs []workerproto.MCPServer) error {
	if len(configs) > 4 {
		return errors.New("MCP configuration permits at most four servers")
	}
	ids, names := map[string]bool{}, map[string]bool{}
	for _, config := range configs {
		if ids[config.ID] || names[config.Name] {
			return fmt.Errorf("duplicate MCP server ID or name: %q", config.Name)
		}
		ids[config.ID], names[config.Name] = true, true
		if err := mcpclient.ValidateConfig(config); err != nil {
			return fmt.Errorf("MCP server %q: %w", config.Name, err)
		}
	}
	return nil
}
