package modelcatalog

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
)

// InventoryModel contains provider-advertised model facts only. Connection
// addresses, credentials, proxy selections and user options cannot enter a
// directory cache through this type. Protocol is used only for Copilot's
// per-model endpoint advertisement, never to override a custom API format.
type InventoryModel struct {
	ID                  string
	modelKind           string
	mediaAdapter        string
	protocol            string
	Tools               *bool
	Context             int
	inputLimit          int
	outputLimit         int
	catalogReasoning    bool
	status              string
	inputModalities     []string
	outputModalities    []string
	discoverySource     string
	metadataKnown       bool
	discoveredName      string
	discoveredReasoning *modelcfg.ReasoningCapabilities
}

// InventoryMetadata projects a one-shot discovery result into cacheable facts.
func (m Configured) InventoryMetadata() InventoryModel {
	protocol := ""
	if m.ProviderID == "github-copilot" {
		protocol = m.Provider
	}
	return InventoryModel{
		ID: m.ID, protocol: protocol, modelKind: m.modelKind, mediaAdapter: m.mediaAdapter, Tools: m.Tools, Context: m.Context,
		inputLimit: m.inputLimit, outputLimit: m.outputLimit,
		catalogReasoning: m.catalogReasoning, status: m.status,
		inputModalities: m.inputModalities, outputModalities: m.outputModalities,
		discoverySource: m.discoverySource, metadataKnown: m.metadataKnown,
		discoveredName: m.discoveredName, discoveredReasoning: m.discoveredReasoning,
	}
}

func Inventory(models []Configured) []InventoryModel {
	result := make([]InventoryModel, 0, len(models))
	for _, model := range models {
		result = append(result, model.InventoryMetadata())
	}
	return result
}

// WithInventoryModel assembles a model from this current connection. Call on a
// DiscoveryTemplate for a new ID, or on the saved anchor to retain restrictions.
func (m Configured) WithInventoryModel(remote InventoryModel) Configured {
	m.ID = remote.ID
	if m.ProviderID == "github-copilot" {
		m.Provider, m.BaseURL = remote.protocol, CopilotRoot(m.BaseURL)
		if m.Provider == "anthropic" {
			m.BaseURL += "/v1"
		}
		m.catalogResolved, m.wireID = false, ""
	} else if LocalProvider(m.ProviderID) {
		m.Provider, m.BaseURL = "openai-chat", strings.TrimRight(m.BaseURL, "/")
		if m.APIKey == "" {
			m.APIKey = "-"
		}
	}
	if m.Key == "" {
		identity := m.ConnectionID
		if m.ProviderID == "github-copilot" {
			if identity == "" {
				identity = CopilotSourceID(m)
			}
			hash := sha256.Sum256([]byte(identity + "\x00" + m.ID))
			m.Key = fmt.Sprintf("copilot-%x", hash[:6])
		} else {
			if identity == "" {
				identity = m.Provider + "\x00" + m.ProviderID + "\x00" + m.BaseURL
			}
			hash := sha256.Sum256([]byte(identity + "\x00" + m.ID))
			m.Key = fmt.Sprintf("remote-%x", hash[:10])
		}
	}
	return m.WithDiscoveryMetadata(remote)
}

func (s LocalSource) Identity() string {
	hash := sha256.Sum256([]byte(s.kind + "\x00" + s.baseURL + "\x00" + s.apiKey))
	return fmt.Sprintf("%x", hash)
}

func (s LocalSource) WithInventoryModel(remote InventoryModel) Configured {
	hash := sha256.Sum256([]byte(remote.ID))
	connection := Configured{Key: fmt.Sprintf("%s-%x", s.kind, hash[:6]), ProviderID: s.kind, BaseURL: s.baseURL, APIKey: s.apiKey}
	return connection.WithInventoryModel(remote)
}

// GenerationChoice reads only advertised model facts, never credentials.
func (m InventoryModel) GenerationChoice(kind string) (provider.OutputMetadata, string, bool) {
	if !generationOutput(kind) || m.modelKind != kind || m.mediaAdapter == "" || m.status != "" && m.status != "active" && m.status != "deprecated" {
		return provider.OutputMetadata{}, "", false
	}
	return provider.OutputMetadata{ID: m.ID, Name: m.discoveredName, Kind: kind, Input: m.inputModalities, Output: m.outputModalities}, m.mediaAdapter, true
}

func generationOutput(kind string) bool {
	return kind == modelcfg.ImageOutput || kind == modelcfg.VideoOutput || kind == modelcfg.DecisionOutput
}

func (m Configured) chatInventoryEligible() bool {
	return m.ChatEligible() && (m.inputModalities == nil || slices.Contains(m.inputModalities, "text")) && (m.outputModalities == nil || slices.Contains(m.outputModalities, "text"))
}

func (m InventoryModel) ChatEligible() bool {
	return (m.modelKind == "" || m.modelKind == "chat") && (m.inputModalities == nil || slices.Contains(m.inputModalities, "text")) && (m.outputModalities == nil || slices.Contains(m.outputModalities, "text"))
}
