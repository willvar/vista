package provider

import (
	"embed"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	modelcfg "vista/internal/model"
)

//go:embed catalog_providers.csv catalog_overrides.csv
var catalogData embed.FS

type CatalogProvider struct {
	ID string
	// Model metadata comes from the active OMP + models.dev catalog.
	Name             string
	Package          string
	BaseURL          string
	WireModelID      string
	KeyNames         []string
	Protocol         string
	Tools            bool
	Reasoning        bool
	ReasoningEfforts []string
	InputModalities  []string
	OutputModalities []string
	ContextLimit     int
	InputLimit       int
	OutputLimit      int
	Status           string
	ResponsesRoute   modelcfg.ResponsesRoute
}

var catalogOnce sync.Once
var catalogProviders map[string]CatalogProvider
var catalogOverrides map[string]CatalogProvider
var catalogCredentialEnv map[string]bool
var catalogError error

// CatalogTextModelIDs lists models with an implemented text route. The setup
// picker uses this inventory; it does not imply account access to every model.
func CatalogTextModelIDs(providerID string) []string {
	return CurrentOMPCatalog().TextModelIDs(providerID)
}

func (catalog *OMPCatalogSnapshot) TextModelIDs(providerID string) []string {
	catalogOnce.Do(loadCatalogProviders)
	if catalogError != nil {
		return nil
	}
	var ids []string
	for _, id := range catalog.modelIDs(providerID) {
		entry, err := catalog.resolveModel(providerID, id)
		if err == nil && entry.Protocol != "" && (entry.Status == "active" || entry.Status == "deprecated") && containsCatalogModality(entry.InputModalities, "text") && containsCatalogModality(entry.OutputModalities, "text") {
			ids = append(ids, id)
		}
	}
	return ids
}

func containsCatalogModality(items []string, modality string) bool {
	for _, item := range items {
		if item == modality {
			return true
		}
	}
	return false
}

// IsPinnedCredentialEnvironmentName reports credential variables in the locked
// Provider inventory and native route aliases. Workers must not inherit keys
// for unselected Providers.
func IsPinnedCredentialEnvironmentName(name string) bool {
	catalogOnce.Do(loadCatalogProviders)
	return catalogError != nil || catalogCredentialEnv[name]
}

func LookupCatalogProvider(id string) (CatalogProvider, error) {
	if id == "openai-codex" {
		return CatalogProvider{ID: id, Package: "@ai-sdk/openai", BaseURL: codexBaseURL}, nil
	}
	catalogOnce.Do(loadCatalogProviders)
	if catalogError != nil {
		return CatalogProvider{}, catalogError
	}
	item, ok := catalogProviders[id]
	if !ok {
		return CatalogProvider{}, errors.New("Provider has no built-in route")
	}
	return item, nil
}

func ResolveCatalogModel(providerID, modelID string) (CatalogProvider, error) {
	return CurrentOMPCatalog().resolveModel(providerID, modelID)
}

func (catalog *OMPCatalogSnapshot) resolveModel(providerID, modelID string) (CatalogProvider, error) {
	item, err := LookupCatalogProvider(providerID)
	if err != nil {
		return CatalogProvider{}, err
	}
	omp, ok := catalog.model(providerID, modelID)
	if !ok {
		return CatalogProvider{}, fmt.Errorf("model %q is absent from the catalog for Provider %q; use a custom provider for unlisted models", modelID, providerID)
	}
	if !ompTextModel(omp) {
		return CatalogProvider{}, fmt.Errorf("OMP model %q cannot provide a supported text conversation", modelID)
	}
	item.Name = omp.Name
	item.Tools = omp.SupportsTools == nil || *omp.SupportsTools
	item.Reasoning = omp.Reasoning
	item.InputModalities = append([]string(nil), omp.Input...)
	item.OutputModalities = []string{"text"}
	item.ContextLimit = omp.ContextWindow
	item.OutputLimit = omp.MaxTokens
	item.Status = "active"
	item.ResponsesRoute = ompResponsesRoute(omp.API)
	if omp.Thinking != nil {
		item.ReasoningEfforts = append([]string(nil), omp.Thinking.Efforts...)
	}
	// Gateways with several wire APIs use OMP's discriminator. Native service
	// adapters still own their endpoint, credentials and request shape.
	if ZenCatalogProvider(providerID) || providerID == "openai-codex" {
		item.Package = ompPackage(omp.API, item.Package)
	}
	if override, ok := catalogOverrides[providerID+"\x00"+modelID]; ok {
		item.Package = override.Package
		if override.BaseURL != "" {
			item.BaseURL = override.BaseURL
		}
	}
	item.Protocol = catalogProtocol(item.Package)
	if providerID == "gitlab" {
		item.Protocol, item.WireModelID = gitlabModelRoute(modelID)
	}
	if providerID == "github-copilot" {
		item.Protocol = copilotProtocol(modelID)
	}
	if providerID == "amazon-bedrock" && omp.API == "openai-responses" {
		item.Package = "@ai-sdk/amazon-bedrock/mantle"
		item.BaseURL = "https://bedrock-mantle.${AWS_REGION}.api.aws/openai/v1"
	}
	if providerID == "google-vertex" {
		switch omp.API {
		case "anthropic-messages":
			item.Package = "@ai-sdk/google-vertex/anthropic"
		case "google-vertex":
			item.Package = "@ai-sdk/google-vertex"
		case "openai-completions":
			item.Package = "@ai-sdk/openai-compatible"
		}
		item.Protocol = catalogProtocol(item.Package)
	}
	if item.Package == "@ai-sdk/amazon-bedrock/mantle" {
		if modelID == "openai.gpt-oss-20b" || modelID == "openai.gpt-oss-120b" {
			item.Protocol = "bedrock-mantle-chat"
		} else {
			item.Protocol = "bedrock-mantle-responses"
		}
	}
	if item.Package == "@aihubmix/ai-sdk-provider" {
		switch {
		case strings.HasPrefix(modelID, "claude-"):
			item.Protocol = "anthropic"
			item.BaseURL = "https://aihubmix.com/v1"
		case (strings.HasPrefix(modelID, "gemini") || strings.HasPrefix(modelID, "imagen")) && !strings.HasSuffix(modelID, "-nothink") && !strings.HasSuffix(modelID, "-search"):
			item.Protocol = "gemini"
			item.BaseURL = "https://aihubmix.com/gemini/v1beta"
		default:
			item.Protocol = "openai-chat"
			item.BaseURL = "https://aihubmix.com/v1"
		}
	}
	if item.Protocol == "openai-chat" {
		item.BaseURL = strings.TrimSuffix(item.BaseURL, "/chat/completions")
	}
	return item, nil
}

func ompResponsesRoute(api string) modelcfg.ResponsesRoute {
	if api == "openai-codex-responses" {
		return modelcfg.ResponsesRouteCodex
	}
	return modelcfg.ResponsesRouteUnknown
}

func ZenCatalogProvider(providerID string) bool {
	return providerID == "opencode" || providerID == "opencode-go"
}

func ompPackage(api, fallback string) string {
	switch api {
	case "openai-completions", "openrouter":
		return "@ai-sdk/openai-compatible"
	case "anthropic-messages":
		return "@ai-sdk/anthropic"
	case "google-generative-ai", "google-gemini-cli":
		return "@ai-sdk/google"
	case "openai-responses", "openai-codex-responses", "azure-openai-responses":
		return "@ai-sdk/openai"
	default:
		return fallback
	}
}

func catalogProtocol(pkg string) string {
	switch pkg {
	case "@ai-sdk/openai-compatible", "@ai-sdk/cerebras", "@ai-sdk/deepinfra", "@ai-sdk/groq", "@ai-sdk/perplexity", "@ai-sdk/togetherai", "@ai-sdk/vercel", "@openrouter/ai-sdk-provider", "@qvac/ai-sdk-provider", "@saladtechnologies-oss/ai-sdk-provider", "ai-gateway-provider", "merge-gateway-ai-sdk-provider", "venice-ai-sdk-provider":
		return "openai-chat"
	case "@ai-sdk/anthropic":
		return "anthropic"
	case "@ai-sdk/cohere":
		return "cohere"
	case "@ai-sdk/amazon-bedrock":
		return "bedrock"
	case "@ai-sdk/google-vertex":
		return "vertex-gemini"
	case "@ai-sdk/google-vertex/anthropic":
		return "vertex-anthropic"
	case "@ai-sdk/gateway":
		return "vercel-gateway"
	case "@ai-sdk/openai", "@ai-sdk/xai":
		return "openai-responses"
	case "@ai-sdk/azure":
		return "openai-responses"
	case "@ai-sdk/google":
		return "gemini"
	case "@ai-sdk/mistral":
		return "mistral"
	case "watsonx-ai-provider":
		return "watsonx"
	case "@jerome-benoit/sap-ai-provider-v2":
		return "sap-ai-core"
	default:
		return ""
	}
}

func loadCatalogProviders() {
	file, err := catalogData.Open("catalog_providers.csv")
	if err != nil {
		catalogError = err
		return
	}
	defer file.Close()
	reader := csv.NewReader(file)
	if _, err := reader.Read(); err != nil {
		catalogError = err
		return
	}
	catalogProviders = map[string]CatalogProvider{}
	catalogCredentialEnv = map[string]bool{}
	for {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || len(row) != 4 {
			catalogError = errors.New("invalid pinned Provider catalog")
			return
		}
		item := CatalogProvider{ID: row[0], Package: row[1], BaseURL: row[2], KeyNames: strings.Split(row[3], "|")}
		// OpenCode V2 maps these compatible inventory entries to native Chat
		// routes with versioned bases that differ from the upstream snapshot.
		switch item.ID {
		case "deepseek":
			item.BaseURL = "https://api.deepseek.com/v1"
		case "fireworks-ai":
			item.BaseURL = "https://api.fireworks.ai/inference/v1"
		case "baseten":
			item.BaseURL = "https://inference.baseten.co/v1"
		}
		// The pinned inventory and OpenCode V2's native routes occasionally use
		// different credential variable names.
		switch item.ID {
		case "meta":
			item.KeyNames = append(item.KeyNames, "META_API_KEY")
		case "moonshotai", "moonshotai-cn":
			item.KeyNames = append(item.KeyNames, "MOONSHOTAI_API_KEY")
		case "alibaba", "alibaba-cn":
			item.KeyNames = append(item.KeyNames, "ALIBABA_API_KEY")
		case "zai", "zai-coding-plan", "zhipuai", "zhipuai-coding-plan":
			item.KeyNames = append(item.KeyNames, "ZAI_API_KEY")
		}
		for _, name := range item.KeyNames {
			for _, suffix := range []string{"_API_KEY", "_APIKEY", "_KEY", "_TOKEN", "_SECRET", "_PAT"} {
				if strings.HasSuffix(name, suffix) {
					catalogCredentialEnv[name] = true
					break
				}
			}
		}
		if item.BaseURL == "" {
			switch item.Package {
			case "@ai-sdk/cerebras":
				item.BaseURL = "https://api.cerebras.ai/v1"
			case "@ai-sdk/deepinfra":
				item.BaseURL = "https://api.deepinfra.com/v1/openai"
			case "@ai-sdk/groq":
				item.BaseURL = "https://api.groq.com/openai/v1"
			case "@ai-sdk/perplexity":
				item.BaseURL = "https://api.perplexity.ai"
			case "@ai-sdk/togetherai":
				item.BaseURL = "https://api.together.xyz/v1"
			case "venice-ai-sdk-provider":
				item.BaseURL = "https://api.venice.ai/api/v1"
			case "@ai-sdk/xai":
				item.BaseURL = "https://api.x.ai/v1"
			case "@ai-sdk/gateway":
				item.BaseURL = "https://ai-gateway.vercel.sh/v3/ai"
			case "@ai-sdk/vercel":
				item.BaseURL = "https://api.v0.dev/v1"
			case "@ai-sdk/cohere":
				item.BaseURL = "https://api.cohere.com/v2"
			case "@saladtechnologies-oss/ai-sdk-provider":
				item.BaseURL = "https://ai.salad.cloud/v1"
			case "watsonx-ai-provider":
				item.BaseURL = "https://us-south.ml.cloud.ibm.com"
			}
		}
		item.Protocol = catalogProtocol(item.Package)
		if item.Protocol == "openai-chat" {
			item.BaseURL = strings.TrimSuffix(item.BaseURL, "/chat/completions")
		}
		catalogProviders[item.ID] = item
	}
	file, err = catalogData.Open("catalog_overrides.csv")
	if err != nil {
		catalogError = err
		return
	}
	defer file.Close()
	reader = csv.NewReader(file)
	if _, err := reader.Read(); err != nil {
		catalogError = err
		return
	}
	catalogOverrides = map[string]CatalogProvider{}
	for {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || len(row) != 4 {
			catalogError = errors.New("invalid pinned model route overrides")
			return
		}
		catalogOverrides[row[0]+"\x00"+row[1]] = CatalogProvider{Package: row[2], BaseURL: row[3]}
	}
}
