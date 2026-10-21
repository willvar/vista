package modelcatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"vista/internal/platform"
	"vista/internal/provider"
	"vista/internal/useragent"
)

var customModelDiscoveryClient = &http.Client{
	Timeout:       5 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("model endpoint redirected") },
}

func SelectableZenTextModel(providerID, id string) bool {
	if providerID != "opencode" && providerID != "opencode-go" {
		return false
	}
	entry, err := provider.ResolveCatalogModel(providerID, id)
	return err == nil && (entry.Status == "active" || entry.Status == "deprecated") && slices.Contains(entry.InputModalities, "text") && slices.Contains(entry.OutputModalities, "text") && entry.Protocol != ""
}

func ReasoningOptionsFor(providerID, id string) []string {
	return provider.ModelReasoningOptionsFor(providerID, id)
}

func Editable(model Configured) bool {
	setup, ok := setupFor(SetupID(model))
	if !ok {
		return false
	}
	for field, value := range map[string]string{"baseUrl": model.BaseURL, "region": model.Region, "project": model.Project, "location": model.Location, "resourceName": model.ResourceName, "deploymentId": model.DeploymentID, "apiVersion": model.APIVersion} {
		if value != "" && !slices.Contains(setup.Fields, field) {
			return false
		}
	}
	data, _ := json.Marshal(model)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	for name := range fields {
		switch name {
		case "key", "connectionId", "name", "provider", "providerId", "id", "baseUrl", "apiKey", "authentication", "headers", "accessToken", "auth", "resourceName", "deploymentId", "apiVersion", "region", "project", "location":
		case "tools", "context":
			if model.ConnectionID == "" || !LocalProvider(model.ProviderID) && model.ProviderID != "github-copilot" {
				return false
			}
		case "copilotAuth":
			if model.ProviderID != "github-copilot" {
				return false
			}
		default:
			return false // Preserve advanced file settings instead of dropping them.
		}
	}
	return true
}

func FetchCustomModelIDs(ctx context.Context, endpoint, key string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("could not request model list")
	}
	req.Header.Set("Accept", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	response, err := useragent.Do(customModelDiscoveryClient, req)
	if err != nil {
		return nil, errors.New("model list is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("model list returned HTTP " + strconv.Itoa(response.StatusCode))
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(payload) > 4<<20 {
		return nil, errors.New("model list response exceeds 4 MiB")
	}
	var inventory struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &inventory); err != nil || inventory.Data == nil {
		return nil, errors.New("model list has an unsupported format")
	}
	if len(inventory.Data) > 4096 {
		return nil, errors.New("model list has more than 4096 entries")
	}
	seen := make(map[string]bool)
	ids := make([]string, 0, len(inventory.Data))
	for _, item := range inventory.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" || len(id) > 200 || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func ZenProvider(providerID string) bool {
	return providerID == "opencode" || providerID == "opencode-go"
}

func SavedProviderKey(catalog Catalog, matches func(Configured) bool) (string, error) {
	var selected *Configured
	for _, model := range catalog.Models {
		if !matches(model) || model.APIKey == "" {
			continue
		}
		if selected != nil && !SameConnection(*selected, model) {
			return "", errors.New("multiple provider connections match; select a connection or provide an API key")
		}
		copy := model
		selected = &copy
	}
	if selected == nil {
		return "", nil
	}
	return selected.APIKey, nil
}

func FetchZenModelIDs(ctx context.Context, providerID, key string) ([]string, error) {
	if !ZenProvider(providerID) || key == "" {
		return nil, errors.New("enter your OpenCode API key to load available models")
	}
	entry, err := provider.LookupCatalogProvider(providerID)
	if err != nil {
		return nil, errors.New("OpenCode model endpoint is unavailable")
	}
	return FetchCustomModelIDs(ctx, strings.TrimRight(entry.BaseURL, "/")+"/models", key)
}

func ValidateZenModels(ctx context.Context, providerID, key string, ids []string) error {
	available, err := FetchZenModelIDs(ctx, providerID, key)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if !slices.Contains(available, id) {
			return errors.New("model is absent from the current OpenCode /models list; refresh the list")
		}
	}
	return nil
}

func ValidCustomProviderName(name string) bool {
	if len(name) > 64 || strings.TrimSpace(name) != name {
		return false
	}
	for _, char := range name {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func GeneratedModelKey(providerName, id string, used map[string]bool) string {
	slug := func(value string) string {
		var stem strings.Builder
		for _, char := range strings.ToLower(value) {
			if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '_' || char == '-' {
				stem.WriteRune(char)
			} else {
				stem.WriteByte('-')
			}
		}
		return strings.Trim(stem.String(), "-")
	}
	provider := slug(providerName)
	if provider == "" {
		provider = "custom"
	}
	provider = provider[:min(len(provider), 24)]
	model := slug(id)
	model = model[:min(len(model), 59-len(provider))]
	base := provider + "-" + model
	if !used[base] {
		return base
	}
	for suffix := 2; ; suffix++ {
		end := 64 - len(strconv.Itoa(suffix)) - 1
		candidate := base[:min(len(base), end)] + "-" + strconv.Itoa(suffix)
		if !used[candidate] {
			return candidate
		}
	}
}

// writeFile atomically replaces the private configuration after validation.
func writeFile(dir string, data []byte) error {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || !platform.PrivateMode(info.Mode()) {
		return errors.New("Vista configuration directory must be private")
	}
	file, err := os.CreateTemp(dir, ".models-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if _, err := io.Copy(file, bytes.NewReader(data)); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, "models.json"))
}
func SameConnection(first, second Configured) bool {
	if first.ConnectionID != "" || second.ConnectionID != "" {
		return first.ConnectionID != "" && first.ConnectionID == second.ConnectionID
	}
	if first.APIKey != second.APIKey || first.Authentication != second.Authentication || first.Name != second.Name || !maps.Equal(first.Headers, second.Headers) {
		return false
	}
	if ZenProvider(first.ProviderID) {
		return first.ProviderID == second.ProviderID
	}
	sameID := first.ProviderID == second.ProviderID || (first.ProviderID == "" || first.ProviderID == "openai") && (second.ProviderID == "" || second.ProviderID == "openai")
	return first.Provider == second.Provider && sameID && first.BaseURL == second.BaseURL
}

// Save validates the resolved copy, but writes only the raw user configuration.
func Save(dir string, raw Catalog) (Catalog, error) {
	resolved := raw
	resolved.Models = append([]Configured(nil), raw.Models...)
	if err := ResolveCatalog(&resolved); err != nil {
		return Catalog{}, err
	}
	if err := Validate(resolved); err != nil {
		return Catalog{}, err
	}
	data, err := json.Marshal(raw)
	if err != nil || len(data) > 32<<10 {
		return Catalog{}, errors.New("model settings exceed 32 KiB")
	}
	if err := writeFile(dir, data); err != nil {
		return Catalog{}, errors.New("could not save model settings")
	}
	return bindCodexCredentials(dir, expandConnectionInventories(raw, resolved)), nil
}
