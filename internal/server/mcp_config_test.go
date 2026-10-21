package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"vista/internal/workerproto"
)

func TestMCPConfigRequiresPrivateValidatedFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "mcp.json")
	if err := os.WriteFile(path, []byte(`[{"id":"local","name":"local","transport":"http","endpoint":"http://127.0.0.1:7000/mcp","token":"secret"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	configs, err := loadMCPConfig(root)
	if err != nil || len(configs) != 1 || configs[0].Token != "secret" {
		t.Fatalf("valid config: %+v %v", configs, err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMCPConfig(root); err == nil {
		t.Fatal("public credential file was accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`[{"id":"bad","name":"bad","transport":"http","endpoint":"http://example.com","token":"secret"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMCPConfig(root); err == nil {
		t.Fatal("unsafe remote token endpoint was accepted")
	}
	if err := os.WriteFile(path, []byte(`[{"id":"remote","name":"remote","transport":"http","endpoint":"https://example.com/mcp","oauth":true,"clientId":"vista-client"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	configs, err = loadMCPConfig(root)
	if err != nil || len(configs) != 1 || !configs[0].OAuth || configs[0].ClientID != "vista-client" {
		t.Fatalf("valid OAuth config: %+v %v", configs, err)
	}
}

func TestMCPConfigRequiresUniqueStableIDsAndNames(t *testing.T) {
	config := workerproto.MCPServer{ID: "stable-id", Name: "我的 MCP 服务", Transport: "http", Endpoint: "https://example.com/mcp"}
	for _, tc := range []struct {
		name  string
		id    string
		label string
	}{
		{"missing ID", "", "Other"},
		{"unsafe ID", "../unsafe", "Other"},
		{"duplicate ID", config.ID, "Other"},
		{"duplicate name", "other-id", config.Name},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other := config
			other.ID, other.Name = tc.id, tc.label
			data, err := json.Marshal([]workerproto.MCPServer{config, other})
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "mcp.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadMCPConfig(dir); err == nil {
				t.Fatal("invalid MCP identity was accepted")
			}
		})
	}
}
