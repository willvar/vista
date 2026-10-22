package server

import (
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"vista/internal/mcpclient"
	"vista/internal/opencodeconfig"
	"vista/internal/workerproto"
)

type openCodeMCP struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command"`
	CWD         string            `json:"cwd"`
	Environment map[string]string `json:"environment"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	OAuth       json.RawMessage   `json:"oauth"`
	Disabled    bool              `json:"disabled"`
	Codemode    *bool             `json:"codemode"`
	Timeout     json.RawMessage   `json:"timeout"`
	Protocol    string            `json:"protocol"`
}

func readOpenCodeMCP(input extensionImportInput) (extensionImportPreview, error) {
	source, result, fingerprint, err := readExtensionSource(input)
	if err != nil {
		return result, err
	}
	servers := map[string]any{}
	failures := map[string]bool{}
	globalTimeout := false
	for _, document := range source.Documents {
		if experimental, ok := document.Value["experimental"].(map[string]any); ok && experimental["mcp_timeout"] != nil {
			globalTimeout = true
		}
		value, exists := document.Value["mcp"]
		if !exists {
			continue
		}
		mcp, ok := value.(map[string]any)
		if !ok {
			return result, errors.New("invalid OpenCode mcp configuration")
		}
		entries, timeout, err := openCodeMCPEntries(mcp)
		if err != nil {
			return result, err
		}
		globalTimeout = globalTimeout || timeout
		for name, value := range entries {
			resolved, err := opencodeconfig.ResolveReferences(value, filepath.Dir(document.Path), source.Home)
			if err != nil {
				failures[name] = true
			}
			servers[name] = opencodeconfig.Merge(servers[name], resolved)
		}
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		entry := extensionImportEntry{ID: name, Name: name}
		data, _ := json.Marshal(servers[name])
		var source openCodeMCP
		switch {
		case failures[name]:
			entry.Reason = "reference"
		case decodeSettingsRequest(strings.NewReader(string(data)), &source) != nil:
			entry.Reason = "unsupported"
		case globalTimeout || len(source.Timeout) > 0 && string(source.Timeout) != "{}" && string(source.Timeout) != "null" || source.Codemode != nil || source.Protocol != "" && source.Protocol != "legacy":
			entry.Reason = "unsupported"
		default:
			entry = translateOpenCodeMCP(entry, source, result.Directory)
		}
		result.Entries = append(result.Entries, entry)
	}
	return finishExtensionPreview(result, fingerprint), nil
}

// OpenCode v2 normalizes both flat server entries and mcp.servers. Native
// entries replace flat entries with the same name within one document.
func openCodeMCPEntries(mcp map[string]any) (map[string]any, bool, error) {
	entries := map[string]any{}
	var native map[string]any
	timeout := false
	for name, value := range mcp {
		server, _ := value.(map[string]any)
		direct := server != nil && (server["type"] == "local" || server["type"] == "remote")
		if name == "servers" && !direct {
			if value != nil {
				var ok bool
				native, ok = value.(map[string]any)
				if !ok {
					return nil, false, errors.New("invalid OpenCode mcp.servers configuration")
				}
			}
			continue
		}
		if name == "timeout" && !direct {
			settings, ok := value.(map[string]any)
			timeout = value != nil && (!ok || len(settings) > 0)
			continue
		}
		if server != nil {
			server = maps.Clone(server)
			if enabled, ok := server["enabled"].(bool); ok {
				server["disabled"] = !enabled
				delete(server, "enabled")
			}
			if oauth, ok := server["oauth"].(map[string]any); ok {
				oauth = maps.Clone(oauth)
				for old, current := range map[string]string{"clientId": "client_id", "clientSecret": "client_secret"} {
					if value, exists := oauth[old]; exists {
						oauth[current] = value
						delete(oauth, old)
					}
				}
				server["oauth"] = oauth
			}
			value = server
		}
		entries[name] = value
	}
	maps.Copy(entries, native)
	return entries, timeout, nil
}

func translateOpenCodeMCP(entry extensionImportEntry, source openCodeMCP, directory string) extensionImportEntry {
	config := workerproto.MCPServer{ID: "preview", Name: entry.Name, Disabled: source.Disabled}
	entry.Disabled = source.Disabled
	switch source.Type {
	case "local":
		if len(source.Environment) > 0 || len(source.Headers) > 0 || len(source.Command) == 0 || len(source.OAuth) > 0 || source.URL != "" {
			entry.Reason = "unsupported"
			break
		}
		config.Transport, config.Directory = "stdio", directory
		if source.CWD != "" {
			config.Directory = source.CWD
			if !filepath.IsAbs(config.Directory) {
				config.Directory = filepath.Join(directory, config.Directory)
			}
		}
		command := source.Command[0]
		if !filepath.IsAbs(command) {
			if strings.ContainsAny(command, `/\`) {
				command = filepath.Join(config.Directory, command)
			} else {
				var err error
				command, err = exec.LookPath(command)
				if err != nil {
					entry.Reason = "command"
					break
				}
			}
		}
		config.Command, config.Args = command, source.Command[1:]
		entry.Location = command
	case "remote":
		if len(source.Environment) > 0 || len(source.Command) > 0 || source.CWD != "" {
			entry.Reason = "unsupported"
			break
		}
		config.Transport, config.Endpoint = "http", source.URL
		if location, err := url.Parse(source.URL); err == nil {
			location.User, location.RawQuery, location.Fragment = nil, "", ""
			entry.Location = location.String()
		}
		for key, value := range source.Headers {
			if strings.EqualFold(key, "Authorization") && strings.HasPrefix(value, "Bearer ") && config.Token == "" {
				config.Token = strings.TrimPrefix(value, "Bearer ")
				continue
			}
			if config.Headers == nil {
				config.Headers = map[string]string{}
			}
			config.Headers[key] = value
		}
		if len(source.OAuth) > 0 && string(source.OAuth) != "false" {
			var oauth struct {
				ClientID     string `json:"client_id"`
				ClientSecret string `json:"client_secret"`
			}
			if config.Token != "" || decodeSettingsRequest(strings.NewReader(string(source.OAuth)), &oauth) != nil {
				entry.Reason = "unsupported"
				break
			}
			config.OAuth, config.ClientID, config.ClientSecret = true, oauth.ClientID, oauth.ClientSecret
			entry.Warning = "oauth"
		} else if len(source.OAuth) == 0 && config.Token == "" && len(config.Headers) == 0 {
			entry.Warning = "auth"
		}
	default:
		entry.Reason = "unsupported"
	}
	if entry.Reason == "" && mcpclient.ValidateConfig(config) != nil {
		entry.Reason = "invalid"
	}
	entry.config = config
	return entry
}
