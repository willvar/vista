package modelcatalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	_ "github.com/glebarez/go-sqlite"

	"vista/internal/codexauth"
	"vista/internal/copilotauth"
	"vista/internal/opencodeconfig"
	"vista/internal/provider"
)

// Import previews never serialize credentials. Confirmation rereads the source
// and checks its fingerprint before making any connection changes.
type ConnectionImport struct {
	ID               string                `json:"id"`
	Name             string                `json:"name"`
	ProviderID       string                `json:"providerId,omitempty"`
	BaseURL          string                `json:"baseUrl,omitempty"`
	Reason           string                `json:"reason,omitempty"`
	CredentialSource string                `json:"credentialSource,omitempty"`
	CredentialGroup  string                `json:"credentialGroup,omitempty"`
	Input            ConnectionInput       `json:"-"`
	Credential       *codexauth.Credential `json:"-"`
}

type OpenCodeImport struct {
	ConfigPaths     []string           `json:"configPaths"`
	CredentialPaths []string           `json:"credentialPaths"`
	Fingerprint     string             `json:"fingerprint"`
	Connections     []ConnectionImport `json:"connections"`
	Plugins         []string           `json:"plugins"`
}

type openCodeAuth struct {
	Type     string `json:"type"`
	MethodID string `json:"methodID"`
	Key      string `json:"key"`
	Access   string `json:"access"`
	Refresh  string `json:"refresh"`
	Expires  int64  `json:"expires"`
	Metadata struct {
		AccountID     string `json:"accountID"`
		EnterpriseURL string `json:"enterpriseUrl"`
		APIEndpoint   string `json:"apiEndpoint"`
	} `json:"metadata"`
}

type openCodeModel struct {
	ModelID       string            `json:"modelID"`
	Package       string            `json:"package"`
	Settings      map[string]any    `json:"settings"`
	Headers       map[string]string `json:"headers"`
	Body          map[string]any    `json:"body"`
	Compatibility map[string]any    `json:"compatibility"`
	Disabled      bool              `json:"disabled"`
}

type openCodeProvider struct {
	Canonical     string                   `json:"canonical"`
	Name          string                   `json:"name"`
	Env           []string                 `json:"env"`
	Package       string                   `json:"package"`
	Settings      map[string]any           `json:"settings"`
	Headers       map[string]string        `json:"headers"`
	Body          map[string]any           `json:"body"`
	Compatibility map[string]any           `json:"compatibility"`
	Models        map[string]openCodeModel `json:"models"`
}

// Declarative OpenCode v1/v2 providers and portable credentials are read.
// Plugins are reported, never loaded or executed. Paths follow OpenCode's XDG
// layout on all platforms; explicit paths support other installations.
func ReadOpenCodeImport(ctx context.Context, configPath, credentialPath string) (OpenCodeImport, error) {
	source, err := opencodeconfig.Read(configPath)
	if err != nil {
		return OpenCodeImport{}, err
	}
	home := source.Home
	result := OpenCodeImport{CredentialPaths: []string{}, ConfigPaths: []string{}, Connections: []ConnectionImport{}, Plugins: []string{}}
	providers := map[string]any{}
	referenceFailures := map[string]bool{}
	fingerprint := sha256.New()
	for _, document := range source.Documents {
		path, data, part := document.Path, document.Data, document.Value
		_, _ = fingerprint.Write([]byte(path + "\x00"))
		_, _ = fingerprint.Write(data)
		entries, err := opencodeconfig.Providers(part)
		if err != nil {
			return result, err
		}
		for id, value := range entries {
			resolved, err := opencodeconfig.ResolveReferences(value, filepath.Dir(path), home)
			if err != nil {
				referenceFailures[id] = true
				continue
			}
			providers[id] = opencodeconfig.Merge(providers[id], resolved)
		}
		for _, plugin := range opencodeconfig.Plugins(part) {
			value, ok := plugin.(map[string]any)
			if !ok {
				continue
			}
			name, _ := value["package"].(string)
			// Package URLs may carry credentials; show only their location.
			if parsed, err := url.Parse(name); err == nil && parsed.IsAbs() {
				parsed.User, parsed.RawQuery, parsed.Fragment = nil, "", ""
				name = parsed.String()
			}
			if name != "" && !slices.Contains(result.Plugins, name) {
				result.Plugins = append(result.Plugins, name)
			}
		}
		result.ConfigPaths = append(result.ConfigPaths, path)
	}
	stores, err := readOpenCodeCredentialStores(ctx, source, credentialPath)
	if err != nil {
		return result, err
	}
	ids := map[string]bool{}
	for id := range providers {
		ids[id] = true
	}
	for id := range referenceFailures {
		ids[id] = true
	}
	for _, store := range stores {
		result.CredentialPaths = append(result.CredentialPaths, store.Path)
		_, _ = fingerprint.Write([]byte(store.Path + "\x00"))
		encoded, _ := json.Marshal(store.Values)
		_, _ = fingerprint.Write(encoded)
		for id := range store.Values {
			ids[id] = true
		}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		_, _ = fingerprint.Write([]byte(id + "\x00"))
		encoded, _ := json.Marshal(providers[id])
		_, _ = fingerprint.Write(encoded)
		if referenceFailures[id] {
			result.Connections = append(result.Connections, ConnectionImport{ID: id, Name: id, Reason: "reference"})
			continue
		}
		candidates := []ConnectionImport{}
		for _, store := range stores {
			auth, exists := store.Values[id]
			if !exists {
				continue
			}
			entry := openCodeConnection(id, encoded, auth)
			entry.CredentialSource = filepath.Base(store.Path)
			if slices.ContainsFunc(candidates, func(other ConnectionImport) bool {
				return entry.Reason == other.Reason && reflect.DeepEqual(entry.Input, other.Input) && reflect.DeepEqual(entry.Credential, other.Credential)
			}) {
				continue
			}
			candidates = append(candidates, entry)
		}
		if len(candidates) == 0 {
			candidates = append(candidates, openCodeConnection(id, encoded, nil))
		}
		for _, entry := range candidates {
			if len(candidates) > 1 {
				entry.CredentialGroup = id
				digest := sha256.Sum256([]byte(id + "\x00" + entry.CredentialSource))
				entry.ID = "credential-" + hex.EncodeToString(digest[:])
			} else {
				entry.CredentialSource = ""
			}
			result.Connections = append(result.Connections, entry)
		}
	}
	result.Fingerprint = hex.EncodeToString(fingerprint.Sum(nil))
	return result, nil
}

func readOpenCodeCredentials(ctx context.Context, path string, explicit bool) (map[string]json.RawMessage, error) {
	result := map[string]json.RawMessage{}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) && !explicit {
		return result, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("could not read OpenCode v2 credentials database")
	}
	// Reuse Vista's SQLite driver, without schema creation or migrations.
	absolute := filepath.ToSlash(path)
	if !strings.HasPrefix(absolute, "/") {
		absolute = "/" + absolute
	}
	location := url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", location.String())
	if err != nil {
		return nil, errors.New("could not open OpenCode v2 credentials database")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var tables int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'credential'`).Scan(&tables); err != nil {
		return nil, errors.New("could not read OpenCode credentials database")
	}
	if tables == 0 && !explicit {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT integration_id, value FROM credential WHERE integration_id IS NOT NULL ORDER BY active DESC, time_created DESC, id DESC LIMIT 4097`)
	if err != nil {
		return nil, errors.New("could not read OpenCode v2 credential table")
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		var id, value string
		if err := rows.Scan(&id, &value); err != nil || count > 4096 || len(value) > 64<<10 {
			return nil, errors.New("invalid or oversized OpenCode v2 credential table")
		}
		if _, exists := result[id]; !exists && id != "" {
			result[id] = json.RawMessage(value)
		}
	}
	if rows.Err() != nil {
		return nil, errors.New("could not read OpenCode v2 credentials")
	}
	return result, nil
}

func openCodeConnection(id string, config, credential json.RawMessage) ConnectionImport {
	entry := ConnectionImport{ID: id, Name: id}
	var source openCodeProvider
	var auth openCodeAuth
	credential = bytes.TrimSpace(credential)
	if len(config) > 0 && string(config) != "null" && (config[0] != '{' || json.Unmarshal(config, &source) != nil) || len(credential) > 0 && (credential[0] != '{' || json.Unmarshal(credential, &auth) != nil) {
		entry.Reason = "invalid"
		return entry
	}
	canonical := source.Canonical
	if canonical == "" {
		canonical = id
	}
	input := ConnectionInput{Name: source.Name, Provider: "catalog", ProviderID: canonical, CredentialMode: "apiKey", Headers: source.Headers}
	setup, builtIn := setupFor(canonical)
	if input.Name == "" && builtIn {
		input.Name = setup.Name
	}
	if input.Name == "" {
		input.Name = id
	}
	entry.Name = input.Name
	// Connection-level body/SDK behavior cannot be silently lost in translation.
	if len(source.Body) > 0 || len(source.Compatibility) > 0 {
		entry.Reason = "unsupported"
		return entry
	}
	for key, value := range source.Settings {
		if key != "apiKey" && key != "baseURL" || value != nil && !isOpenCodeString(value) {
			entry.Reason = "unsupported"
			return entry
		}
	}
	input.APIKey, _ = source.Settings["apiKey"].(string)
	base, _ := source.Settings["baseURL"].(string)
	if input.APIKey == "" && auth.Type == "key" {
		input.APIKey = auth.Key
	}
	if input.APIKey == "" && auth.Type == "" {
		for _, env := range source.Env {
			if input.APIKey = os.Getenv(env); input.APIKey != "" {
				break
			}
		}
	}
	if auth.Type == "oauth" && input.APIKey == "" {
		if source.Package != "" || len(source.Headers) > 0 || base != "" && canonical != "github-copilot" {
			entry.Reason = "unsupported"
			return entry
		}
		switch canonical {
		case "openai":
			if auth.MethodID != "chatgpt-browser" && auth.MethodID != "chatgpt-headless" {
				entry.Reason = "oauth"
				return entry
			}
			input.ProviderID, input.CredentialMode = "openai-codex", "oauth"
			if source.Name == "" {
				input.Name, entry.Name = "OpenAI Codex", "OpenAI Codex"
			}
			account := auth.Metadata.AccountID
			if account == "" {
				account = openCodeAccountID(auth.Access)
			}
			if account == "" || auth.Refresh == "" || auth.Access == "" || auth.Expires <= 0 || len(auth.Access) > 16<<10 || len(auth.Refresh) > 16<<10 || len(account) > 256 {
				entry.Reason = "credentials"
				return entry
			}
			entry.Credential = &codexauth.Credential{AccessToken: auth.Access, RefreshToken: auth.Refresh, AccountID: account, ExpiresAt: time.UnixMilli(auth.Expires)}
		case "github-copilot":
			if auth.MethodID != "device" {
				entry.Reason = "oauth"
				return entry
			}
			input.APIKey, input.CredentialMode = auth.Refresh, "oauth"
			if base == "" {
				base = auth.Metadata.APIEndpoint
			}
			if auth.Metadata.EnterpriseURL != "" {
				input.CopilotAuth = &copilotauth.Options{EnterpriseURL: auth.Metadata.EnterpriseURL}
			}
		default:
			entry.Reason = "oauth"
			return entry
		}
	} else if input.APIKey == "" && auth.Type != "" && auth.Type != "key" {
		entry.Reason = "oauth"
		return entry
	}
	for _, model := range source.Models {
		if model.Disabled {
			continue
		}
		if model.Package != "" && model.Package != source.Package || len(model.Settings) > 0 || len(model.Headers) > 0 || len(model.Body) > 0 || len(model.Compatibility) > 0 {
			entry.Reason = "unsupported"
			return entry
		}
	}
	if builtIn && source.Package == "" && (base == "" || LocalProvider(canonical) || canonical == "github-copilot") {
		if slices.Contains([]string{"azure", "amazon-bedrock", "google-vertex"}, canonical) {
			entry.Reason = "unsupported"
			return entry
		}
		if LocalProvider(canonical) || canonical == "github-copilot" {
			input.BaseURL = base
		}
		if LocalProvider(canonical) && input.APIKey == "" {
			input.CredentialMode = "default"
		}
		if len(setup.CredentialModes) > 0 && input.CredentialMode != "oauth" && !slices.Contains(setup.CredentialModes, input.CredentialMode) {
			entry.Reason = "unsupported"
			return entry
		}
	} else {
		if entry.Credential != nil || auth.Type == "oauth" && input.APIKey == "" {
			entry.Reason = "oauth"
			return entry
		}
		pack := source.Package
		catalog, catalogErr := provider.LookupCatalogProvider(canonical)
		if pack == "" && catalogErr == nil {
			pack = catalog.Package
		}
		protocol := openCodeProtocol(pack)
		if protocol == "" {
			entry.Reason = "unsupported"
			return entry
		}
		if base == "" && catalogErr == nil {
			base = catalog.BaseURL
		}
		input.Provider, input.ProviderID, input.BaseURL = protocol, "custom", base
		input.CredentialMode = "bearer"
		if protocol == "anthropic" {
			input.CredentialMode = "x-api-key"
		}
		if protocol == "gemini" {
			input.CredentialMode = "x-goog-api-key"
		}
		if input.APIKey == "" {
			input.CredentialMode = "none"
		}
		for modelID, model := range source.Models {
			if model.Disabled {
				continue
			}
			if model.ModelID != "" {
				modelID = model.ModelID
			}
			input.IDs = append(input.IDs, modelID)
		}
		sort.Strings(input.IDs)
		input.IDs = slices.Compact(input.IDs)
	}
	if input.APIKey == "" && entry.Credential == nil && input.CredentialMode != "none" && input.CredentialMode != "default" {
		entry.Reason = "credentials"
		return entry
	}
	if len(input.IDs) > 16 {
		entry.Reason = "limit"
		return entry
	}
	if _, err := prepareSetup(&input, nil); err != nil {
		entry.Reason = "invalid"
		return entry
	}
	entry.Input, entry.ProviderID, entry.BaseURL, entry.Name = input, input.ProviderID, input.BaseURL, input.Name
	return entry
}

func isOpenCodeString(value any) bool { _, ok := value.(string); return ok }

func openCodeProtocol(pack string) string {
	pack = strings.TrimPrefix(pack, "aisdk:")
	pack = strings.Replace(pack, "@opencode-ai/ai/", "@opencode/ai/", 1)
	return map[string]string{
		"@opencode/ai/providers/openai":                      "openai-responses",
		"@opencode/ai/providers/openai/responses":            "openai-responses",
		"@opencode/ai/providers/openai-compatible/responses": "openai-responses",
		"@opencode/ai/providers/openai-compatible-responses": "openai-responses",
		"@opencode/ai/providers/openai/chat":                 "openai-chat",
		"@opencode/ai/providers/openai-compatible":           "openai-chat",
		"@opencode/ai/providers/anthropic":                   "anthropic",
		"@opencode/ai/providers/anthropic-compatible":        "anthropic",
		"@opencode/ai/providers/google":                      "gemini",
		"@opencode/ai/providers/cohere":                      "cohere",
		"@opencode/ai/providers/cohere/chat":                 "cohere",
		"@ai-sdk/openai-compatible":                          "openai-chat", "@ai-sdk/openai": "openai-responses",
		"@ai-sdk/anthropic": "anthropic", "@ai-sdk/google": "gemini", "@ai-sdk/cohere": "cohere",
	}[pack]
}

// MarkExisting skips destination duplicates without replacing existing accounts.
func (preview *OpenCodeImport) MarkExisting(raw Catalog) {
	for i := range preview.Connections {
		entry := &preview.Connections[i]
		if entry.Reason != "" {
			continue
		}
		for _, model := range raw.Models {
			if model.ProviderID == entry.Input.ProviderID && strings.TrimRight(model.BaseURL, "/") == strings.TrimRight(entry.Input.BaseURL, "/") && (entry.Input.ProviderID != "custom" || model.Provider == entry.Input.Provider) {
				entry.Reason = "existing"
				break
			}
		}
	}
}

func openCodeAccountID(access string) string {
	parts := strings.Split(access, ".")
	if len(parts) != 3 {
		return ""
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		AccountID     string `json:"chatgpt_account_id"`
		Organizations []struct {
			ID string `json:"id"`
		} `json:"organizations"`
		Auth struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err != nil || json.Unmarshal(data, &claims) != nil {
		return ""
	}
	if claims.AccountID != "" {
		return claims.AccountID
	}
	if claims.Auth.AccountID != "" {
		return claims.Auth.AccountID
	}
	if len(claims.Organizations) > 0 {
		return claims.Organizations[0].ID
	}
	return ""
}
