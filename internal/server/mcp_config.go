package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"vista/internal/mcpclient"
	"vista/internal/workerproto"

	"vista/internal/platform"
)

func loadMCPConfig(dir string) ([]workerproto.MCPServer, error) {
	path := filepath.Join(dir, "mcp.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !platform.PrivateMode(info.Mode()) || info.Size() > 8<<10 {
		return nil, errors.New("mcp.json must be a regular 0600 file of at most 8 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (8<<10)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8<<10 {
		return nil, errors.New("mcp.json exceeds 8 KiB")
	}
	var configs []workerproto.MCPServer
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configs); err != nil {
		return nil, fmt.Errorf("parse mcp.json: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("mcp.json has extra data")
	}
	if len(configs) > 4 {
		return nil, errors.New("mcp.json permits at most four servers")
	}
	ids, names := map[string]bool{}, map[string]bool{}
	for _, config := range configs {
		if ids[config.ID] || names[config.Name] {
			return nil, fmt.Errorf("duplicate MCP server ID or name: %q", config.Name)
		}
		ids[config.ID], names[config.Name] = true, true
		if err := mcpclient.ValidateConfig(config); err != nil {
			return nil, fmt.Errorf("MCP server %q: %w", config.Name, err)
		}
	}
	return configs, nil
}
