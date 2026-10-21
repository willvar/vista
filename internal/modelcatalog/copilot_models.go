package modelcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"vista/internal/copilotauth"
)

const maxLocalModels = 64

var copilotDiscoveryClient = &http.Client{
	Timeout: 5 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("Copilot model endpoint redirected")
	},
}

func CopilotRoot(base string) string {
	base = strings.TrimRight(base, "/")
	return strings.TrimSuffix(base, "/v1")
}

func CopilotSourceID(model Configured) string {
	sum := sha256.Sum256([]byte(model.ConnectionID + "\x00" + model.Name + "\x00" + CopilotRoot(model.BaseURL) + "\x00" + model.APIKey))
	return fmt.Sprintf("%x", sum)
}

func DiscoverCopilotModels(parent context.Context, source Configured) ([]Configured, error) {
	base := CopilotRoot(source.BaseURL)
	if !ValidProviderBaseURL(base) {
		return nil, errors.New("Copilot model discovery requires HTTPS or loopback HTTP")
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+source.APIKey)
	req.Header.Set("X-GitHub-Api-Version", "2026-08-01")
	copilotauth.SetAPIHeaders(req.Header)
	response, err := copilotDiscoveryClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Copilot models HTTP %d", response.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > 1<<20 {
		return nil, errors.New("Copilot model response exceeds 1 MiB")
	}
	var inventory struct {
		Data []struct {
			ID                 string   `json:"id"`
			ModelPickerEnabled bool     `json:"model_picker_enabled"`
			SupportedEndpoints []string `json:"supported_endpoints"`
			Policy             struct {
				State string `json:"state"`
			} `json:"policy"`
			Capabilities struct {
				Limits struct {
					Context int `json:"max_context_window_tokens"`
					Input   int `json:"max_prompt_tokens"`
					Output  int `json:"max_output_tokens"`
					Vision  struct {
						SupportedMediaTypes []string `json:"supported_media_types"`
					} `json:"vision"`
				} `json:"limits"`
				Supports struct {
					Tools           *bool    `json:"tool_calls"`
					ReasoningEffort []string `json:"reasoning_effort"`
					Vision          bool     `json:"vision"`
				} `json:"supports"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &inventory); err != nil {
		return nil, err
	}
	if inventory.Data == nil {
		return nil, errors.New("Copilot model response has no data array")
	}
	if len(inventory.Data) > maxLocalModels {
		return nil, errors.New("Copilot has over 64 models; discovery limit exceeded")
	}
	models := make([]Configured, 0, len(inventory.Data))
	seen := map[string]bool{}
	for _, item := range inventory.Data {
		limit := item.Capabilities.Limits
		if !item.ModelPickerEnabled || item.Policy.State == "disabled" || item.ID == "" || len(item.ID) > 200 || seen[item.ID] || limit.Input <= 0 || limit.Output <= 0 || item.Capabilities.Supports.Tools == nil {
			continue
		}
		protocol := ""
		for _, endpoint := range item.SupportedEndpoints {
			if endpoint == "/v1/messages" {
				protocol = "anthropic"
				break
			}
			if endpoint == "/responses" && protocol != "anthropic" {
				protocol = "openai-responses"
			} else if endpoint == "/chat/completions" && protocol == "" {
				protocol = "openai-chat"
			}
		}
		if protocol == "" {
			continue
		}
		contextLimit := limit.Context
		if contextLimit <= 0 {
			contextLimit = limit.Input
		}
		inputModalities := []string{"text"}
		vision := item.Capabilities.Supports.Vision
		for _, mediaType := range limit.Vision.SupportedMediaTypes {
			if mediaType == "image/png" || mediaType == "image/jpeg" || mediaType == "image/webp" || mediaType == "image/gif" {
				vision = true
				break
			}
		}
		if vision {
			inputModalities = append(inputModalities, "image")
		}
		identity := source.ConnectionID
		if identity == "" {
			identity = CopilotSourceID(source)
		}
		hash := sha256.Sum256([]byte(identity + "\x00" + item.ID))
		modelBase := base
		if protocol == "anthropic" {
			modelBase += "/v1"
		}
		models = append(models, Configured{
			Key: fmt.Sprintf("copilot-%x", hash[:6]), ConnectionID: source.ConnectionID, Name: source.Name, Provider: protocol,
			ProviderID: "github-copilot", ID: item.ID, BaseURL: modelBase,
			APIKey: source.APIKey, Tools: item.Capabilities.Supports.Tools,
			Context: contextLimit, inputLimit: limit.Input, outputLimit: limit.Output,
			catalogReasoning: len(item.Capabilities.Supports.ReasoningEffort) > 0,
			status:           "active", inputModalities: inputModalities, outputModalities: []string{"text"},
			discoverySource: "github-copilot",
		})
		seen[item.ID] = true
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
