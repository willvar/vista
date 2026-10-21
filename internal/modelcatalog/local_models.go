package modelcatalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"vista/internal/useragent"
)

var discoveryClient = &http.Client{
	Timeout:       time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("local model endpoint redirected") },
}

type LocalSource struct {
	kind, baseURL, apiKey string
}

func LocalSources() []LocalSource {
	ollama := os.Getenv("VISTA_OLLAMA_BASE_URL")
	if ollama == "" {
		ollama = "http://127.0.0.1:11434/v1"
	}
	lmstudio := os.Getenv("VISTA_LMSTUDIO_BASE_URL")
	if lmstudio == "" {
		lmstudio = "http://127.0.0.1:1234/v1"
	}
	vllm := os.Getenv("VISTA_VLLM_BASE_URL")
	if vllm == "" {
		vllm = "http://127.0.0.1:8000/v1"
	}
	return []LocalSource{{"ollama", ollama, os.Getenv("VISTA_OLLAMA_API_KEY")}, {"lmstudio", lmstudio, os.Getenv("VISTA_LMSTUDIO_API_KEY")}, {"vllm", vllm, os.Getenv("VISTA_VLLM_API_KEY")}}
}

func Identity(model Configured) string {
	if model.ConnectionID != "" {
		return model.ConnectionID + "\x00" + model.ID
	}
	identity := model.ProviderID + "\x00" + model.ID
	if model.ProviderID == "custom" || (model.Provider == "openai-chat" || model.Provider == "openai-responses") && (model.ProviderID == "" || model.ProviderID == "openai") {
		identity = model.Provider + "\x00" + model.Authentication + "\x00" + model.BaseURL + "\x00" + model.ID
	}
	if model.ProviderID == "github-copilot" {
		identity += "\x00" + model.APIKey + "\x00" + CopilotRoot(model.BaseURL)
	}
	return identity
}

func DiscoverLocalModels(parent context.Context, source LocalSource) ([]Configured, error) {
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	base, endpoint, err := localEndpoint(source)
	if err != nil {
		return nil, err
	}
	switch source.kind {
	case "ollama":
		return discoverOllama(ctx, source, base, endpoint)
	case "lmstudio":
		return discoverLMStudio(ctx, source, base, endpoint)
	case "vllm":
		return discoverVLLM(ctx, source, base, endpoint)
	default:
		return nil, errors.New("unknown local model source")
	}
}

func localEndpoint(source LocalSource) (string, string, error) {
	u, err := url.Parse(source.baseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && localLoopback(u.Hostname()))) {
		return "", "", errors.New("local model base URL requires HTTPS or loopback HTTP")
	}
	base := strings.TrimRight(u.String(), "/")
	prefix := strings.TrimRight(u.Path, "/")
	prefix = strings.TrimSuffix(prefix, "/v1")
	u.RawPath = ""
	if source.kind == "vllm" {
		u.Path = strings.TrimRight(u.Path, "/") + "/models"
	} else if source.kind == "ollama" {
		u.Path = prefix + "/api/tags"
	} else {
		u.Path = prefix + "/api/v1/models"
	}
	return base, u.String(), nil
}

func discoveryJSON(ctx context.Context, method, endpoint, key string, body any, output any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil || len(encoded) > 4096 {
			return errors.New("local model request exceeds 4 KiB")
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	response, err := useragent.Do(discoveryClient, req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("local model HTTP %d", response.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(payload) > 1<<20 {
		return errors.New("local model response exceeds 1 MiB")
	}
	return json.Unmarshal(payload, output)
}

func discoveredModel(source LocalSource, base, id string, tools bool, context int) Configured {
	hash := sha256.Sum256([]byte(id))
	key := fmt.Sprintf("%s-%x", source.kind, hash[:6])
	apiKey := source.apiKey
	if apiKey == "" {
		apiKey = "-" // OpenAI Chat requires a nonempty key; local endpoints ignore this placeholder.
	}
	return Configured{Key: key, Provider: "openai-chat", ProviderID: source.kind, ID: id, BaseURL: base, APIKey: apiKey, Tools: &tools, Context: context}.WithDiscoverySource(source.kind)
}

func discoverOllama(ctx context.Context, source LocalSource, base, endpoint string) ([]Configured, error) {
	var tags struct {
		Models []struct {
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := discoveryJSON(ctx, http.MethodGet, endpoint, source.apiKey, nil, &tags); err != nil {
		return nil, err
	}
	if len(tags.Models) > maxLocalModels {
		return nil, errors.New("Ollama has over 64 models; discovery limit exceeded")
	}
	showURL := strings.TrimSuffix(endpoint, "/tags") + "/show"
	models := make([]Configured, len(tags.Models))
	var group sync.WaitGroup
	var showsRead atomic.Int32
	var showsFailed atomic.Int32
	semaphore := make(chan struct{}, 4)
	for index, item := range tags.Models {
		if item.Model == "" || len(item.Model) > 200 {
			continue
		}
		group.Add(1)
		go func(index int, id string) {
			defer group.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			var show struct {
				Capabilities []string                   `json:"capabilities"`
				ModelInfo    map[string]json.RawMessage `json:"model_info"`
			}
			if err := discoveryJSON(ctx, http.MethodPost, showURL, source.apiKey, map[string]string{"model": id}, &show); err != nil {
				showsFailed.Add(1)
				return
			}
			showsRead.Add(1)
			completion, tools := false, false
			for _, capability := range show.Capabilities {
				completion = completion || capability == "completion"
				tools = tools || capability == "tools"
			}
			if !completion {
				return
			}
			context := 0
			for key, raw := range show.ModelInfo {
				if strings.HasSuffix(key, ".context_length") {
					var value int
					if json.Unmarshal(raw, &value) == nil && value > 0 && (context == 0 || value < context) {
						context = value
					}
				}
			}
			models[index] = discoveredModel(source, base, id, tools, context)
		}(index, item.Model)
	}
	group.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if showsFailed.Load() > 0 {
		return nil, errors.New("Ollama model details unavailable for part of inventory")
	}
	if len(tags.Models) > 0 && showsRead.Load() == 0 {
		return nil, errors.New("Ollama model details unavailable")
	}
	result := make([]Configured, 0, len(models))
	seen := map[string]bool{}
	for _, model := range models {
		if model.ID != "" && !seen[model.Key] {
			result = append(result, model)
			seen[model.Key] = true
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func discoverLMStudio(ctx context.Context, source LocalSource, base, endpoint string) ([]Configured, error) {
	var list struct {
		Models []struct {
			Type             string `json:"type"`
			Key              string `json:"key"`
			MaxContextLength int    `json:"max_context_length"`
			LoadedInstances  []struct {
				Config struct {
					ContextLength int `json:"context_length"`
				} `json:"config"`
			} `json:"loaded_instances"`
			Capabilities *struct {
				TrainedForToolUse bool `json:"trained_for_tool_use"`
			} `json:"capabilities"`
		} `json:"models"`
	}
	if err := discoveryJSON(ctx, http.MethodGet, endpoint, source.apiKey, nil, &list); err != nil {
		return nil, err
	}
	if len(list.Models) > maxLocalModels {
		return nil, errors.New("LM Studio has over 64 models; discovery limit exceeded")
	}
	result := make([]Configured, 0, len(list.Models))
	seen := map[string]bool{}
	for _, item := range list.Models {
		if item.Type != "llm" || item.Key == "" || len(item.Key) > 200 {
			continue
		}
		context := item.MaxContextLength
		for _, loaded := range item.LoadedInstances {
			if value := loaded.Config.ContextLength; value > 0 && (context <= 0 || value < context) {
				context = value
			}
		}
		tools := item.Capabilities != nil && item.Capabilities.TrainedForToolUse
		model := discoveredModel(source, base, item.Key, tools, context)
		if !seen[model.Key] {
			result = append(result, model)
			seen[model.Key] = true
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func discoverVLLM(ctx context.Context, source LocalSource, base, endpoint string) ([]Configured, error) {
	health, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	health.Path = strings.TrimSuffix(strings.TrimRight(health.Path, "/"), "/v1") + "/health"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, health.String(), nil)
	if err != nil {
		return nil, err
	}
	if source.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+source.apiKey)
	}
	response, err := useragent.Do(discoveryClient, request)
	if err != nil {
		return nil, err
	}
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("vLLM health HTTP %d", response.StatusCode)
	}
	var list struct {
		Data []struct {
			ID          string `json:"id"`
			OwnedBy     string `json:"owned_by"`
			MaxModelLen int    `json:"max_model_len"`
		} `json:"data"`
	}
	if err := discoveryJSON(ctx, http.MethodGet, endpoint, source.apiKey, nil, &list); err != nil {
		return nil, err
	}
	if len(list.Data) > maxLocalModels {
		return nil, errors.New("vLLM has over 64 models; discovery limit exceeded")
	}
	result := make([]Configured, 0, len(list.Data))
	seen := map[string]bool{}
	for _, item := range list.Data {
		if item.OwnedBy != "vllm" || item.ID == "" || len(item.ID) > 200 {
			continue
		}
		context := item.MaxModelLen
		if context < 0 {
			context = 0
		}
		model := discoveredModel(source, base, item.ID, false, context)
		// vLLM's model list does not advertise tool parser configuration.
		// Unknown capability follows the compatible API path; it is not a
		// declaration that every served model is unable to call tools.
		model.Tools = nil
		if !seen[model.Key] {
			result = append(result, model)
			seen[model.Key] = true
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// Kind identifies the local service without exposing its credential.
func (s LocalSource) Kind() string { return s.kind }
