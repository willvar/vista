package modelcatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"vista/internal/codexauth"
	"vista/internal/copilotauth"
	modelcfg "vista/internal/model"
	"vista/internal/provider"

	"vista/internal/platform"
)

type Configured struct {
	Key                 string                  `json:"key"`
	ConnectionID        string                  `json:"connectionId,omitempty"`
	Name                string                  `json:"name,omitempty"`
	Provider            string                  `json:"provider"`
	ProviderID          string                  `json:"providerId,omitempty"`
	ID                  string                  `json:"id"`
	ResponsesRoute      modelcfg.ResponsesRoute `json:"responsesRoute,omitempty"`
	BaseURL             string                  `json:"baseUrl,omitempty"`
	Route               string                  `json:"route,omitempty"`
	APIKey              string                  `json:"apiKey"`
	Authentication      string                  `json:"authentication,omitempty"`
	Headers             map[string]string       `json:"headers,omitempty"`
	CopilotAuth         *copilotauth.Options    `json:"copilotAuth,omitempty"`
	AuthToken           string                  `json:"authToken,omitempty"`
	BedrockAuth         string                  `json:"auth,omitempty"`
	AccessToken         string                  `json:"accessToken,omitempty"`
	AccountID           string                  `json:"accountId,omitempty"`
	GatewayID           string                  `json:"gatewayId,omitempty"`
	ResourceName        string                  `json:"resourceName,omitempty"`
	APIVersion          string                  `json:"apiVersion,omitempty"`
	UseCompletionURLs   *bool                   `json:"useCompletionUrls,omitempty"`
	UseDeploymentURLs   *bool                   `json:"useDeploymentBasedUrls,omitempty"`
	AppCode             string                  `json:"appCode,omitempty"`
	Region              string                  `json:"region,omitempty"`
	WorkspaceID         string                  `json:"workspaceID,omitempty"`
	baseURLExplicit     bool
	Project             string `json:"project,omitempty"`
	Organization        string `json:"organization,omitempty"`
	ProjectID           string `json:"projectId,omitempty"`
	DeploymentID        string `json:"deploymentId,omitempty"`
	ResourceGroup       string `json:"resourceGroup,omitempty"`
	Location            string `json:"location,omitempty"`
	InstanceURL         string `json:"instanceUrl,omitempty"`
	wireID              string
	Tools               *bool                        `json:"tools,omitempty"`
	Context             int                          `json:"context,omitempty"`
	ReasoningSummary    string                       `json:"reasoningSummary,omitempty"`
	ReasoningEffort     string                       `json:"reasoningEffort,omitempty"`
	Effort              string                       `json:"effort,omitempty"`
	SafetyIdentifier    string                       `json:"safetyIdentifier,omitempty"`
	PromptCacheKey      string                       `json:"promptCacheKey,omitempty"`
	Thinking            *modelcfg.ThinkingOption     `json:"thinking,omitempty"`
	EnableThinking      *bool                        `json:"enableThinking,omitempty"`
	ThinkingBudget      *int                         `json:"thinkingBudget,omitempty"`
	ThinkingLevel       string                       `json:"thinkingLevel,omitempty"`
	MaxCompletionTokens *int                         `json:"maxCompletionTokens,omitempty"`
	RepetitionPenalty   *float64                     `json:"repetitionPenalty,omitempty"`
	PreserveThinking    *bool                        `json:"preserveThinking,omitempty"`
	ClearThinking       *bool                        `json:"clearThinking,omitempty"`
	ToolStream          *bool                        `json:"toolStream,omitempty"`
	ParallelToolCalls   *bool                        `json:"parallelToolCalls,omitempty"`
	ZAIDoSample         *bool                        `json:"doSample,omitempty"`
	ChatResponseFormat  *modelcfg.ChatResponseFormat `json:"responseFormat,omitempty"`
	ZAIRequestID        string                       `json:"requestID,omitempty"`
	ZAIUserID           string                       `json:"userID,omitempty"`
	ReasoningSplit      *bool                        `json:"reasoningSplit,omitempty"`
	OpenRouter          *modelcfg.OpenRouterOptions  `json:"openRouter,omitempty"`
	InputModalities     []string                     `json:"inputModalities,omitempty"`
	inputLimit          int
	outputLimit         int
	catalogReasoning    bool
	catalogResolved     bool
	status              string
	inputModalities     []string
	outputModalities    []string
	discoverySource     string
	azureCatalog        bool
	azureNative         bool
	codexCredentialPath string
}

type Catalog struct {
	Default       string       `json:"default"`
	Models        []Configured `json:"models"`
	credentialEnv []string
}

type Info struct {
	Key              string   `json:"key"`
	ConnectionID     string   `json:"connectionId,omitempty"`
	Name             string   `json:"name,omitempty"`
	ModelName        string   `json:"modelName,omitempty"`
	Provider         string   `json:"provider"`
	ProviderID       string   `json:"providerId,omitempty"`
	ID               string   `json:"id"`
	BaseURL          string   `json:"baseUrl,omitempty"`
	Tools            *bool    `json:"tools,omitempty"`
	Context          int      `json:"context,omitempty"`
	InputLimit       int      `json:"inputLimit,omitempty"`
	OutputLimit      int      `json:"outputLimit,omitempty"`
	CatalogReasoning bool     `json:"catalogReasoning,omitempty"`
	Status           string   `json:"status,omitempty"`
	InputModalities  []string `json:"inputModalities,omitempty"`
	OutputModalities []string `json:"outputModalities,omitempty"`
	DiscoverySource  string   `json:"discoverySource,omitempty"`
	ReasoningOptions []string `json:"reasoningOptions,omitempty"`
}

func Load(dir string) (Catalog, error) {
	return LoadWithOMPCatalog(dir, provider.CurrentOMPCatalog())
}

// LoadWithOMPCatalog checks saved connections against a staged catalog update.
func LoadWithOMPCatalog(dir string, snapshot *provider.OMPCatalogSnapshot) (Catalog, error) {
	catalog, exists, err := Read(dir)
	if err != nil {
		return Catalog{}, err
	}
	if !exists {
		return FromEnvironment()
	}
	raw := catalog
	catalog.Models = append([]Configured(nil), raw.Models...)
	if err := resolveWithOMPCatalog(&catalog, snapshot); err != nil {
		return Catalog{}, err
	}
	if err := Validate(catalog); err != nil {
		return Catalog{}, err
	}
	return bindCodexCredentials(dir, expandInventoriesWithOMPCatalog(raw, catalog, snapshot)), nil
}

// Read leaves catalog routes and credentials in their original form,
// so a WebUI edit cannot serialize resolved URLs or environment secrets.
func Read(dir string) (Catalog, bool, error) {
	path := filepath.Join(dir, "models.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Catalog{}, false, nil
	}
	if err != nil {
		return Catalog{}, false, err
	}
	if !info.Mode().IsRegular() || !platform.PrivateMode(info.Mode()) || info.Size() > 32<<10 {
		return Catalog{}, false, errors.New("models.json must be a private regular file of at most 32 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return Catalog{}, false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (32<<10)+1))
	if err != nil || len(data) > 32<<10 {
		return Catalog{}, false, errors.New("models.json exceeds 32 KiB")
	}
	var catalog Catalog
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return Catalog{}, false, fmt.Errorf("parse models.json: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Catalog{}, false, errors.New("models.json has extra data")
	}
	// Read previously saved empty catalogs without restoring their placeholder.
	if catalog.Default == "demo" && len(catalog.Models) == 1 && catalog.Models[0].Key == "demo" && catalog.Models[0].Provider == "" {
		catalog = Catalog{Models: []Configured{}}
	}
	return catalog, true, nil
}

func ResolveCatalog(catalog *Catalog) error {
	return resolveWithOMPCatalog(catalog, provider.CurrentOMPCatalog())
}

func resolveWithOMPCatalog(catalog *Catalog, snapshot *provider.OMPCatalogSnapshot) error {
	for index := range catalog.Models {
		item := &catalog.Models[index]
		resolved, err := snapshot.ResolveConfig(item.providerConfig())
		if err != nil {
			return err
		}
		item.applyProviderResolution(resolved)
		catalog.credentialEnv = append(catalog.credentialEnv, resolved.CredentialEnvironment...)
	}
	return nil
}

func fillMissingModelLimits(model *Configured) {
	limits := provider.ModelLimitsFor(model.ProviderID, model.ID)
	if model.Context == 0 {
		model.Context = limits.Context
	}
	if model.inputLimit == 0 {
		model.inputLimit = limits.Input
	}
	if model.outputLimit == 0 {
		model.outputLimit = limits.Output
	}
}

func ValidProviderBaseURL(value string) bool { return provider.ValidProviderBaseURL(value) }

func ValidCustomProviderBaseURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Scheme == "https" || u.Scheme == "http")
}

func FromEnvironment() (Catalog, error) {
	catalog := Catalog{Models: []Configured{}}
	if key := os.Getenv("VISTA_OPENAI_API_KEY"); key != "" {
		id := os.Getenv("VISTA_OPENAI_MODEL")
		if id == "" {
			id = "gpt-4.1-mini"
		}
		catalog.Models = append(catalog.Models,
			Configured{Key: "openai", Provider: "openai-chat", ProviderID: "openai", ID: id, BaseURL: os.Getenv("VISTA_OPENAI_BASE_URL"), APIKey: key},
			Configured{Key: "openai-responses", Provider: "openai-responses", ProviderID: "openai", ID: id, BaseURL: os.Getenv("VISTA_OPENAI_BASE_URL"), APIKey: key},
		)
	}
	if key := os.Getenv("VISTA_ANTHROPIC_API_KEY"); key != "" {
		id := os.Getenv("VISTA_ANTHROPIC_MODEL")
		if id == "" {
			id = "claude-sonnet-4-5"
		}
		catalog.Models = append(catalog.Models, Configured{Key: "anthropic", Provider: "anthropic", ID: id, BaseURL: os.Getenv("VISTA_ANTHROPIC_BASE_URL"), APIKey: key})
	}
	if key := os.Getenv("VISTA_GEMINI_API_KEY"); key != "" {
		id := os.Getenv("VISTA_GEMINI_MODEL")
		if id == "" {
			id = "gemini-2.5-flash"
		}
		catalog.Models = append(catalog.Models, Configured{Key: "gemini", Provider: "gemini", ProviderID: "google", ID: id, BaseURL: os.Getenv("VISTA_GEMINI_BASE_URL"), APIKey: key})
	}
	if key := os.Getenv("VISTA_MISTRAL_API_KEY"); key != "" {
		id := os.Getenv("VISTA_MISTRAL_MODEL")
		if id == "" {
			id = "codestral-latest"
		}
		catalog.Models = append(catalog.Models, Configured{Key: "mistral", Provider: "mistral", ProviderID: "mistral", ID: id, BaseURL: os.Getenv("VISTA_MISTRAL_BASE_URL"), APIKey: key})
	}
	catalog.Default = os.Getenv("VISTA_DEFAULT_MODEL")
	if catalog.Default == "" && len(catalog.Models) > 0 {
		catalog.Default = catalog.Models[0].Key
	}
	if err := Validate(catalog); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

func Validate(catalog Catalog) error {
	if len(catalog.Models) > 16 {
		return errors.New("configure at most 16 models")
	}
	seen := map[string]bool{}
	for _, item := range catalog.Models {
		if item.CopilotAuth != nil {
			if item.ProviderID != "github-copilot" {
				return errors.New("Copilot login settings are only supported for GitHub Copilot")
			}
			if _, err := item.CopilotAuth.Normalize(); err != nil {
				return err
			}
		}
		if item.ProviderID == "openai-codex" && item.APIKey == "" && codexauth.Path("", item.ConnectionID) == "" {
			return errors.New("Codex requires a saved account connection")
		}
		if item.Key == "" || len(item.Key) > 64 || !ValidCustomProviderName(item.Name) || len(item.ProviderID) > 64 || len(item.ID) > 200 || len(item.BaseURL) > 2048 || len(item.InstanceURL) > 2048 || len(item.APIKey) > 4096 || len(item.AuthToken) > 4096 || item.Context < 0 || !item.ResponsesRoute.Valid() || seen[item.Key] {
			return errors.New("model key or value exceeds limits or is duplicated")
		}
		if err := provider.ValidateCatalogOptions(item.providerConfig(), item.providerMetadata()); err != nil {
			return err
		}
		for _, ch := range item.Key {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
				return errors.New("model key permits only letters, digits, underscores and hyphens")
			}
		}
		seen[item.Key] = true
		for _, ch := range item.ProviderID {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-' || ch == '.') {
				return errors.New("provider ID permits only letters, digits, underscores, hyphens and periods")
			}
		}
		if item.Provider == "" {
			return errors.New("model API format is required")
		}
		if len(item.inputModalities) > 0 && len(item.InputModalities) > 0 {
			return fmt.Errorf("model %q cannot override catalog input modalities", item.Key)
		}
		if len(item.InputModalities) > 0 {
			if !slices.Contains(item.InputModalities, "text") {
				return fmt.Errorf("model %q cannot override catalog input modalities", item.Key)
			}
			for _, modality := range item.InputModalities {
				if modality != "text" && modality != "image" {
					return fmt.Errorf("model %q has unsupported input modality %q", item.Key, modality)
				}
			}
			if slices.Contains(item.InputModalities, "image") && !provider.SupportsImageInput(item.Provider) {
				return fmt.Errorf("model %q uses a protocol without image input", item.Key)
			}
		}
		if err := provider.ValidateCatalogProtocol(item.providerConfig(), item.providerMetadata()); err != nil {
			return err
		}

	}
	if !seen[catalog.Default] && (len(catalog.Models) != 0 || catalog.Default != "") {
		return errors.New("default model key is not configured")
	}
	return nil
}

func (c Catalog) Resolve(key string) (modelcfg.Model, string, error) {
	if len(c.Models) == 0 {
		return modelcfg.Model{}, "", errors.New("connect a model provider first")
	}
	if key == "" {
		key = c.Default
	}
	for _, item := range c.Models {
		if item.Key == key {
			if !item.catalogResolved {
				fillMissingModelLimits(&item)
			}
			model := provider.CatalogModelRequest(item.providerConfig(), item.providerMetadata())
			model.CodexCredentialPath = item.codexCredentialPath
			imageInput := slices.Contains(item.EffectiveInputModalities(), "image") && provider.SupportsImageInput(model.Provider)
			model.ImageInput = &imageInput
			return model, key, nil
		}
	}
	return modelcfg.Model{}, "", fmt.Errorf("model %q is not configured", key)
}

func (c Catalog) Public() []Info {
	result := make([]Info, 0, len(c.Models))
	for _, item := range c.Models {
		result = append(result, item.Info())
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}

// Info projects metadata without exposing configuration credentials.
func (item Configured) Info() Info {
	options := []string(nil)
	if !item.catalogResolved {
		fillMissingModelLimits(&item)
	}
	request := provider.CatalogModelRequest(item.providerConfig(), item.providerMetadata())
	options = provider.SessionReasoningOptions(request)
	baseURL := ""
	if item.ProviderID == "custom" || (item.Provider == "openai-chat" || item.Provider == "openai-responses") && (item.ProviderID == "" || item.ProviderID == "openai") {
		baseURL = item.BaseURL
	}
	return Info{Key: item.Key, ConnectionID: item.ConnectionID, Name: item.Name, ModelName: provider.ModelDisplayNameFor(item.ProviderID, item.ID), Provider: item.Provider, ProviderID: item.ProviderID, ID: item.ID, BaseURL: baseURL, Tools: item.Tools, Context: item.Context, InputLimit: item.inputLimit, OutputLimit: item.outputLimit, CatalogReasoning: item.catalogReasoning, Status: item.status, InputModalities: provider.SupportedInputModalities(item.Provider, item.EffectiveInputModalities()), OutputModalities: item.outputModalities, DiscoverySource: item.discoverySource, ReasoningOptions: options}
}

func (m Configured) EffectiveInputModalities() []string {
	if len(m.inputModalities) > 0 {
		return m.inputModalities
	}
	if len(m.InputModalities) > 0 {
		return m.InputModalities
	}
	// Custom directories often supply only IDs. Resolve their capabilities at
	// read time so metadata refreshes apply without reconnecting the provider.
	return provider.ModelInputModalitiesFor(m.ProviderID, m.ID)
}

func localLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// CredentialEnvironment names must be excluded from worker process environments.
func (c Catalog) CredentialEnvironment() []string { return slices.Clone(c.credentialEnv) }

// WithDiscoverySource labels a live inventory entry without writing derived data to models.json.
func (m Configured) WithDiscoverySource(source string) Configured {
	m.discoverySource = source
	return m
}

// WithStatus updates inventory availability without changing the saved configuration.
func (m Configured) WithStatus(status string) Configured {
	m.status = status
	return m
}

// Available reports whether the inventory permits selecting this model.
func (m Configured) Available() bool {
	return m.status == "" || m.status == "active" || m.status == "deprecated"
}
