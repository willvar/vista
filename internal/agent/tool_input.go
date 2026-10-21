package agent

import (
	"encoding/json"
	"net/url"

	"vista/internal/provider"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

func toolInput(call provider.ToolCall, command workerproto.Command) *workerproto.ToolInput {
	if call.Function.Name != "shell" && call.Function.Name != "start_background" {
		return nil
	}
	var input workerproto.ToolInput
	if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
		return nil
	}
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = 60
		if call.Function.Name == "start_background" {
			input.TimeoutSeconds = 3600
		}
	}
	secrets := commandSecrets(command)
	input.Command = toolInputPreview(input.Command, secrets)
	input.Directory = toolInputPreview(input.Directory, secrets)
	return &input
}

// Full arguments stay in model history. Display metadata must leave room in
// the result frame for output and task identity, including JSON escaping.
func toolInputPreview(value string, secrets []string) string {
	value = tools.DisplayShellCommand(value, secrets)
	preview := jsonTextPreview(value, 4<<10)
	if preview != value {
		preview += "\n[Display truncated; full arguments retained in execution history.]"
	}
	return preview
}

func commandSecrets(command workerproto.Command) []string {
	secrets := []string{command.Model.APIKey, command.Model.AWSAccessID, command.Model.AWSSecret, command.Model.AWSSession}
	if command.Model.OpenRouter != nil {
		for _, key := range command.Model.OpenRouter.APIKeys {
			secrets = append(secrets, key)
		}
	}
	for _, server := range command.MCPServers {
		secrets = append(secrets, server.Token, server.ClientSecret)
	}
	for _, search := range command.SearchProviders {
		secrets = append(secrets, search.APIKey)
	}
	if command.Webfetch.JinaAPIKey != "" {
		secrets = append(secrets, command.Webfetch.JinaAPIKey)
	}
	return secrets
}

func toolSummary(call provider.ToolCall) string {
	var input struct {
		Task         string `json:"task"`
		Path         string `json:"path"`
		Root         string `json:"root"`
		URL          string `json:"url"`
		Query        string `json:"query"`
		Directory    string `json:"directory"`
		Server       string `json:"server"`
		Name         string `json:"name"`
		Text         string `json:"text"`
		PreviousText string `json:"previousText"`
	}
	_ = json.Unmarshal([]byte(call.Function.Arguments), &input)
	if call.Function.Name == "memory" {
		if input.Text != "" {
			return textPrefix(input.Text, 300)
		}
		return textPrefix(input.PreviousText, 300)
	}
	summary := input.Path
	if input.Task != "" {
		summary = input.Task
	}
	if summary == "" {
		summary = input.Root
	}
	if summary == "" {
		if parsed, err := url.Parse(input.URL); err == nil {
			parsed.RawQuery = ""
			parsed.Fragment = ""
			summary = parsed.String()
		}
	}
	if summary == "" {
		summary = input.Query
	}
	if summary == "" {
		summary = input.Directory
	}
	if summary == "" && input.Server != "" {
		summary = input.Server
		if input.Name != "" {
			summary += "/" + input.Name
		}
	}
	return textPrefix(summary, 300)
}
