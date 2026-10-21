package server

import (
	"encoding/json"
	"errors"
	"io"
	"maps"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"vista/internal/workerproto"

	"vista/internal/platform"
)

const searchSettingsFilename = "search.json"
const maxSearchAPIKeyBytes = 4096

type searchSettingsConfig struct {
	APIKeys             map[string]string `json:"apiKeys"`
	Order               []string          `json:"order"`
	FetchOrder          []string          `json:"fetchOrder"`
	JinaAPIKey          string            `json:"jinaApiKey,omitempty"`
	DeepSeekFetchAPIKey string            `json:"deepseekFetchApiKey,omitempty"`
	SummaryLength       int               `json:"summaryLength"`
	FetchPreviewLength  int               `json:"fetchPreviewLength"`
}

type searchProviderSetting struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

func validSearchProvider(name string) bool {
	return name == "exa" || name == "firecrawl" || name == "parallel" || name == "tavily" || name == "deepseek"
}

func validSearchCredential(name string) bool {
	return name == "jina" || name == "deepseek-fetch" || validSearchProvider(name)
}

func validSearchAPIKey(key string) bool {
	return key != "" && len(key) <= maxSearchAPIKeyBytes && utf8.ValidString(key) &&
		strings.IndexFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func validSearchOrder(order []string) bool {
	if len(order) != 5 {
		return false
	}
	seen := make(map[string]bool, len(order))
	for _, name := range order {
		if !validSearchProvider(name) || seen[name] {
			return false
		}
		seen[name] = true
	}
	return true
}

func validWebfetchOrder(order []string) bool {
	if len(order) != 3 {
		return false
	}
	seen := make(map[string]bool, len(order))
	for _, name := range order {
		if name != "vista" && name != "jina" && name != "deepseek" || seen[name] {
			return false
		}
		seen[name] = true
	}
	return true
}

// Initialize once per configuration directory.
// Persist the initial order before serving requests so restarts never reshuffle it.
func loadSearchSettings(dir string) (searchSettingsConfig, error) {
	settings := searchSettingsConfig{SummaryLength: 1000, FetchPreviewLength: 8000}
	file, err := os.Open(filepath.Join(dir, searchSettingsFilename))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return settings, errors.New("could not read search settings")
	}
	if file != nil {
		// The credentials and order fit comfortably within this bounded file.
		data, readErr := io.ReadAll(io.LimitReader(file, (256<<10)+1))
		file.Close()
		if readErr != nil || len(data) > 256<<10 || decodeSettingsRequest(strings.NewReader(string(data)), &settings) != nil {
			return searchSettingsConfig{}, errors.New("invalid search settings")
		}
	}
	for name, key := range settings.APIKeys {
		if !validSearchProvider(name) || !validSearchAPIKey(key) {
			return searchSettingsConfig{}, errors.New("invalid search settings")
		}
	}
	if settings.JinaAPIKey != "" && !validSearchAPIKey(settings.JinaAPIKey) {
		return searchSettingsConfig{}, errors.New("invalid search settings")
	}
	if settings.DeepSeekFetchAPIKey != "" && !validSearchAPIKey(settings.DeepSeekFetchAPIKey) || settings.SummaryLength < 0 || settings.FetchPreviewLength < 0 {
		return searchSettingsConfig{}, errors.New("invalid search settings")
	}
	if settings.APIKeys == nil {
		settings.APIKeys = map[string]string{}
	}
	initialize := settings.Order == nil || settings.FetchOrder == nil
	if settings.Order == nil {
		external := []string{"firecrawl", "parallel", "tavily", "exa", "deepseek"}
		rand.Shuffle(len(external), func(i, j int) { external[i], external[j] = external[j], external[i] })
		settings.Order = external
	} else if !validSearchOrder(settings.Order) {
		return searchSettingsConfig{}, errors.New("invalid search settings")
	}
	if settings.FetchOrder == nil {
		settings.FetchOrder = []string{"vista", "jina", "deepseek"}
	} else if !validWebfetchOrder(settings.FetchOrder) {
		return searchSettingsConfig{}, errors.New("invalid webfetch order")
	}
	if initialize {
		if err := writeSearchSettings(dir, settings); err != nil {
			return searchSettingsConfig{}, errors.New("could not initialize search settings")
		}
	}
	return settings, nil
}

func writeSearchSettings(dir string, settings searchSettingsConfig) error {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || !platform.PrivateMode(info.Mode()) {
		return errors.New("Vista configuration directory must be private")
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".search-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, searchSettingsFilename))
}

// Each run keeps its own order and credentials; changes apply to the next run.
func (a *App) currentSearchProviders() []workerproto.SearchProvider {
	a.searchMu.RLock()
	defer a.searchMu.RUnlock()
	providers := make([]workerproto.SearchProvider, 0, len(a.searchConfig.Order))
	for _, name := range a.searchConfig.Order {
		for _, provider := range a.searchProviders {
			if provider.Name == name {
				if key := a.searchConfig.APIKeys[name]; key != "" {
					provider.APIKey = key
				}
				providers = append(providers, provider)
				break
			}
		}
	}
	return providers
}

func (a *App) currentWebfetchSettings() workerproto.WebfetchConfig {
	a.searchMu.RLock()
	defer a.searchMu.RUnlock()
	return workerproto.WebfetchConfig{Order: append([]string(nil), a.searchConfig.FetchOrder...), JinaAPIKey: a.searchConfig.JinaAPIKey, DeepSeekAPIKey: a.searchConfig.DeepSeekFetchAPIKey, PreviewLength: a.searchConfig.FetchPreviewLength}
}

func (a *App) currentSearchSummaryLength() int {
	a.searchMu.RLock()
	defer a.searchMu.RUnlock()
	return a.searchConfig.SummaryLength
}

func (a *App) searchSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		a.searchMu.RLock()
		defer a.searchMu.RUnlock()
		providers := make([]searchProviderSetting, 0, len(a.searchProviders))
		for _, provider := range a.searchProviders {
			source := "none"
			if a.searchConfig.APIKeys[provider.Name] != "" {
				source = "settings"
			} else if provider.APIKey != "" {
				source = "environment"
			}
			providers = append(providers, searchProviderSetting{Name: provider.Name, Source: source})
		}
		jinaSource := "none"
		if a.searchConfig.JinaAPIKey != "" {
			jinaSource = "settings"
		}
		deepseekSource := "none"
		if a.searchConfig.DeepSeekFetchAPIKey != "" {
			deepseekSource = "settings"
		}
		jsonOut(w, map[string]any{"providers": providers, "order": a.searchConfig.Order, "fetchOrder": a.searchConfig.FetchOrder, "jinaReader": searchProviderSetting{Name: "jina", Source: jinaSource}, "deepseekFetch": searchProviderSetting{Name: "deepseek", Source: deepseekSource}, "summaryLength": a.searchConfig.SummaryLength, "fetchPreviewLength": a.searchConfig.FetchPreviewLength})
	case http.MethodPatch:
		r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		var input struct {
			Order              []string `json:"order"`
			FetchOrder         []string `json:"fetchOrder"`
			SummaryLength      *int     `json:"summaryLength"`
			FetchPreviewLength *int     `json:"fetchPreviewLength"`
		}
		if decodeSettingsRequest(r.Body, &input) != nil || input.Order == nil && input.FetchOrder == nil && input.SummaryLength == nil && input.FetchPreviewLength == nil {
			http.Error(w, "provide order, fetchOrder, summaryLength, or fetchPreviewLength", http.StatusBadRequest)
			return
		}
		if input.Order != nil && !validSearchOrder(input.Order) {
			http.Error(w, "order must contain firecrawl, parallel, tavily, exa, and deepseek exactly once", http.StatusBadRequest)
			return
		}
		if input.FetchOrder != nil && !validWebfetchOrder(input.FetchOrder) {
			http.Error(w, "fetchOrder must contain vista, jina, and deepseek exactly once", http.StatusBadRequest)
			return
		}
		if input.SummaryLength != nil && *input.SummaryLength < 0 {
			http.Error(w, "summaryLength must be a non-negative integer; 0 means unlimited", http.StatusBadRequest)
			return
		}
		if input.FetchPreviewLength != nil && *input.FetchPreviewLength < 0 {
			http.Error(w, "fetchPreviewLength must be a non-negative integer; 0 means unlimited", http.StatusBadRequest)
			return
		}
		a.searchMu.Lock()
		defer a.searchMu.Unlock()
		settings := a.searchConfig
		if input.Order != nil {
			settings.Order = input.Order
		}
		if input.FetchOrder != nil {
			settings.FetchOrder = input.FetchOrder
		}
		if input.SummaryLength != nil {
			settings.SummaryLength = *input.SummaryLength
		}
		if input.FetchPreviewLength != nil {
			settings.FetchPreviewLength = *input.FetchPreviewLength
		}
		if err := writeSearchSettings(a.configDir, settings); err != nil {
			http.Error(w, "could not save search settings", http.StatusInternalServerError)
			return
		}
		a.searchConfig = settings
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPut, http.MethodDelete:
		r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		var input struct {
			Name   string  `json:"name"`
			APIKey *string `json:"apiKey"`
		}
		if decodeSettingsRequest(r.Body, &input) != nil || !validSearchCredential(input.Name) {
			http.Error(w, "invalid search settings request", http.StatusBadRequest)
			return
		}
		key := ""
		if r.Method == http.MethodPut {
			if input.APIKey != nil {
				key = strings.TrimSpace(*input.APIKey)
			}
			if !validSearchAPIKey(key) {
				http.Error(w, "API key must be 1–4096 bytes without whitespace or control characters", http.StatusBadRequest)
				return
			}
		} else if input.APIKey != nil {
			http.Error(w, "invalid search settings request", http.StatusBadRequest)
			return
		}
		a.searchMu.Lock()
		defer a.searchMu.Unlock()
		settings := a.searchConfig
		if input.Name == "deepseek-fetch" {
			settings.DeepSeekFetchAPIKey = key
		} else if input.Name == "jina" {
			if r.Method == http.MethodPut {
				settings.JinaAPIKey = key
			} else {
				settings.JinaAPIKey = ""
			}
		} else {
			keys := maps.Clone(settings.APIKeys)
			if keys == nil {
				keys = map[string]string{}
			}
			if r.Method == http.MethodPut {
				keys[input.Name] = key
			} else {
				delete(keys, input.Name)
			}
			settings.APIKeys = keys
		}
		if err := writeSearchSettings(a.configDir, settings); err != nil {
			http.Error(w, "could not save search settings", http.StatusInternalServerError)
			return
		}
		a.searchConfig = settings
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}
