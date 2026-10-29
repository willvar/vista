// Package mcphost hosts all MCP connections outside the application Server.
// Workers send tool data directly to its private loopback endpoint; the Server
// exchanges only configuration, readiness, and bounded status records.
package mcphost

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"vista/internal/mcpclient"
	"vista/internal/workerproto"
)

const maxControlBytes = 256 << 10
const inlineResultBytes = 32 << 10

type Definition struct {
	Config mcpclient.Config `json:"config"`
	Search bool             `json:"search,omitempty"`
}

// Display names and enable switches do not identify a transport connection.
func Key(def Definition) string {
	def.Config.Name, def.Config.Disabled = "", false
	if def.Search {
		def.Config.ID = "search"
	}
	data, _ := json.Marshal(def)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func Definitions(servers []workerproto.MCPServer, searches []workerproto.SearchProvider) []Definition {
	definitions := make([]Definition, 0, len(servers)+len(searches))
	for _, server := range servers {
		if !server.Disabled {
			definitions = append(definitions, Definition{Config: server})
		}
	}
	for _, search := range searches {
		if config, ok := mcpclient.SearchConfig(search.Name, search.Endpoint, search.APIKey); ok {
			definitions = append(definitions, Definition{Config: config, Search: true})
		}
	}
	return definitions
}

type control struct {
	Endpoint    string       `json:"endpoint,omitempty"`
	Token       string       `json:"token,omitempty"`
	ConfigDir   string       `json:"configDir,omitempty"`
	StateDir    string       `json:"stateDir,omitempty"`
	Revision    uint64       `json:"revision"`
	MemoryLimit int64        `json:"memoryLimit"`
	Definitions []Definition `json:"definitions"`
}

type State struct {
	Status    string    `json:"status"`
	CheckedAt time.Time `json:"checkedAt,omitzero"`
	ToolCount int       `json:"toolCount"`
	Error     string    `json:"error,omitempty"`
}

type Event struct {
	Sequence uint64               `json:"sequence,omitempty"`
	Kind     string               `json:"kind"`
	Endpoint string               `json:"endpoint,omitempty"`
	Revision uint64               `json:"revision,omitempty"`
	Key      string               `json:"key,omitempty"`
	ID       string               `json:"id,omitempty"`
	State    State                `json:"state,omitzero"`
	Metrics  *workerproto.Metrics `json:"metrics,omitempty"`
}

type request struct {
	Definitions []Definition    `json:"definitions,omitempty"`
	Lease       string          `json:"lease,omitempty"`
	Key         string          `json:"key,omitempty"`
	Name        string          `json:"name,omitempty"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
	Cursor      string          `json:"cursor,omitempty"`
	Force       bool            `json:"force,omitempty"`
}

type response struct {
	Data  json.RawMessage `json:"data,omitempty"`
	File  string          `json:"file,omitempty"`
	Bytes int64           `json:"bytes,omitempty"`
	Error string          `json:"error,omitempty"`
}
