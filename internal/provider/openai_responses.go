package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"vista/internal/codexauth"
	modelcfg "vista/internal/model"
)

type responseItem struct {
	ID               string `json:"id"`
	ItemID           string `json:"item_id"`
	Type             string `json:"type"`
	Status           string `json:"status"`
	CallID           string `json:"call_id"`
	Name             string `json:"name"`
	Arguments        string `json:"arguments"`
	EncryptedContent string `json:"encrypted_content"`
	Summary          []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
	Content []struct {
		Type        string               `json:"type"`
		Text        string               `json:"text"`
		Refusal     string               `json:"refusal"`
		Annotations []responseAnnotation `json:"annotations"`
	} `json:"content"`
}

type responseAnnotation struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type responseEvent struct {
	Type         string             `json:"type"`
	ID           string             `json:"id"`
	ItemID       string             `json:"item_id"`
	CallID       string             `json:"call_id"`
	Delta        string             `json:"delta"`
	Text         string             `json:"text"`
	SummaryIndex int                `json:"summary_index"`
	ContentIndex int                `json:"content_index"`
	Arguments    string             `json:"arguments"`
	Annotation   responseAnnotation `json:"annotation"`
	Item         responseItem       `json:"item"`
	Error        json.RawMessage    `json:"error"`
	Response     struct {
		Status string            `json:"status"`
		Output []json.RawMessage `json:"output"`
		Error  json.RawMessage   `json:"error"`
		Usage  *struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
			InputDetails struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			OutputDetails struct {
				ReasoningTokens int64 `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	} `json:"response"`
}

type responseCall struct {
	item responseItem
	args strings.Builder
}

// Full snapshots can repeat a 256 KiB answer with JSON escaping overhead.
// Decoded text, reasoning and tool data retain their own aggregate limits.
const maxResponsesSnapshotEventBytes = 2 << 20

func openAIResponsesRouteDefaults(model modelcfg.Model) bool {
	if model.Provider == "bedrock-mantle-responses" {
		return true
	}
	if model.Provider != "openai-responses" {
		return false
	}
	switch model.ProviderID {
	case "", "custom", "openai", "openai-codex", "azure", "azure-cognitive-services":
		return true
	}
	return false
}

func OpenAIResponsesTurn(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any) (Turn, error) {
	return openAIResponsesTurn(ctx, model, messages, schemas, nil)
}

func openAIResponsesTurn(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any, onText func(string) error) (Turn, error) {
	return openAIResponsesTurnWithReasoning(ctx, model, messages, schemas, onText, nil)
}

func openAIResponsesTurnWithReasoning(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any, onText, onReasoning func(string) error) (Turn, error) {
	if model.CodexCredentialPath != "" {
		if model.ProviderID != "openai-codex" || model.ResponsesRoute != modelcfg.ResponsesRouteCodex || model.BaseURL != "" && model.BaseURL != codexBaseURL {
			return Turn{}, errors.New("Codex account credentials require the official Codex endpoint")
		}
		credential, err := codexauth.NewClient().Token(ctx, model.CodexCredentialPath, "")
		if err != nil {
			return Turn{}, err
		}
		model.APIKey = credential.AccessToken
	}
	if err := validateImageMessages(messages); err != nil {
		return Turn{}, err
	}
	if model.ID == "" || model.APIKey == "" && !model.Custom && !isBedrockMantle(model) {
		return Turn{}, errors.New("OpenAI Responses model and API key are required")
	}
	if err := requireConfiguredGateway(model); err != nil {
		return Turn{}, err
	}
	if err := requireConfiguredAzure(model); err != nil {
		return Turn{}, err
	}
	base := model.BaseURL
	if base == "" {
		if model.ResponsesRoute == modelcfg.ResponsesRouteCodex {
			base = codexBaseURL
		} else {
			base = "https://api.openai.com/v1"
		}
	}
	u, err := url.Parse(base)
	customResponses := model.Custom || model.Provider == "openai-responses" && model.ProviderID == "" && model.BaseURL != ""
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (customResponses || isLoopback(u.Hostname())))) {
		return Turn{}, errors.New("invalid OpenAI Responses base URL")
	}
	input := make([]any, 0, len(messages)*2)
	var instructions []string
	for _, message := range messages {
		switch message.Role {
		case "system":
			instructions = append(instructions, message.Content)
		case "user", "assistant":
			images, err := imagePayloads(message)
			if err != nil {
				return Turn{}, err
			}
			if message.Role == "assistant" && len(message.ResponseOutput) > 0 {
				if !validResponseOutput(message.ResponseOutput, maxResponseOutputBytes, true) {
					return Turn{}, errors.New("invalid previous OpenAI Responses output items")
				}
				for _, raw := range message.ResponseOutput {
					input = append(input, raw)
				}
				continue
			}
			if len(images) > 0 {
				parts := []any{}
				if message.Content != "" {
					parts = append(parts, map[string]any{"type": "input_text", "text": message.Content})
				}
				for _, url := range images {
					parts = append(parts, map[string]any{"type": "input_image", "image_url": url})
				}
				input = append(input, map[string]any{"type": "message", "role": "user", "content": parts})
			} else if message.Content != "" {
				content := any(message.Content)
				// The ChatGPT Codex endpoint follows the OMP wire shape and
				// expects input_text content parts even for a plain text message.
				// The public Responses API accepts either form, so keep the
				// string representation for the other gateways.
				if model.ResponsesRoute == modelcfg.ResponsesRouteCodex && message.Role == "user" {
					content = []map[string]string{{"type": "input_text", "text": message.Content}}
				}
				input = append(input, map[string]any{"type": "message", "role": message.Role, "content": content})
			}
			if message.Role == "assistant" {
				for _, raw := range message.ResponseReasoning {
					if !validResponseReasoningItem(raw) {
						return Turn{}, errors.New("invalid previous OpenAI Responses reasoning item")
					}
					input = append(input, raw)
				}
				for _, call := range message.ToolCalls {
					input = append(input, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
				}
			}
		case "tool":
			if message.ToolCallID == "" {
				return Turn{}, errors.New("OpenAI Responses tool result lacks a call ID")
			}
			input = append(input, map[string]any{"type": "function_call_output", "call_id": message.ToolCallID, "output": message.Content})
		default:
			return Turn{}, errors.New("invalid OpenAI Responses message role")
		}
	}
	tools := make([]map[string]any, 0, len(schemas))
	for _, schema := range schemas {
		if schema["type"] != "function" {
			return Turn{}, errors.New("OpenAI Responses supports only function tool schemas")
		}
		function, ok := schema["function"].(map[string]any)
		if !ok || function["name"] == "" {
			return Turn{}, errors.New("invalid OpenAI Responses function schema")
		}
		tools = append(tools, map[string]any{"type": "function", "name": function["name"], "description": function["description"], "parameters": function["parameters"], "strict": false})
	}
	body := map[string]any{"model": requestModelID(model), "stream": true, "store": false, "input": input}
	if !model.ReasoningDisabled && (openAIResponsesRouteDefaults(model) || model.ProviderID == "meta" || model.ProviderID == "xai") {
		// These routes only return the opaque state needed for a stateless tool
		// continuation when it is explicitly requested.
		body["include"] = []string{"reasoning.encrypted_content"}
	}
	reasoning := map[string]any{}
	if !model.ReasoningDisabled {
		// Visible summaries are opt-in, independently of the effort setting.
		// Use the catalog or explicit reasoning configuration, not a GPT version.
		metadata, _ := ompModelForMetadata(model.ProviderID, model.ID)
		if model.ReasoningEffort != "none" && (metadata.Reasoning || model.ReasoningEffort != "") {
			reasoning["summary"] = "auto"
		}
	}
	if !model.ReasoningDisabled && model.ReasoningSummary != "" {
		if model.ReasoningSummary != "auto" && model.ReasoningSummary != "concise" && model.ReasoningSummary != "detailed" {
			return Turn{}, errors.New("invalid OpenAI Responses reasoning summary option")
		}
		reasoning["summary"] = model.ReasoningSummary
	}
	if !model.ReasoningDisabled && model.ReasoningEffort != "" {
		reasoning["effort"] = model.ReasoningEffort
	}
	if len(reasoning) > 0 {
		body["reasoning"] = reasoning
	}
	if !model.ReasoningDisabled && alibabaProvider(model.ProviderID) && model.EnableThinking != nil {
		body["enable_thinking"] = *model.EnableThinking
	}
	if moonshotProvider(model.ProviderID) && model.SafetyIdentifier != "" {
		body["safety_identifier"] = model.SafetyIdentifier
	}
	if len(instructions) > 0 || model.ResponsesRoute == modelcfg.ResponsesRouteCodex {
		body["instructions"] = strings.Join(instructions, "\n")
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return Turn{}, err
	}
	endpoint := openAIEndpoint(u, "/responses", model)
	if model.ResponsesRoute == modelcfg.ResponsesRouteCodex {
		endpoint = codexResponsesEndpoint(u)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return Turn{}, err
	}
	setOpenAIAuth(request, model)
	setCodexHeaders(request, model)
	copilotHeaders(request, model, messages)
	gatewayHeader(request, model)
	gitlabHeaders(request, model)
	request.Header.Set("Content-Type", "application/json")
	response, err := doModelRequest(request, model)
	if err != nil {
		return Turn{}, err
	}
	if response.StatusCode == http.StatusUnauthorized && model.CodexCredentialPath != "" {
		response.Body.Close()
		credential, refreshErr := codexauth.NewClient().Token(ctx, model.CodexCredentialPath, model.APIKey)
		if refreshErr != nil {
			return Turn{}, refreshErr
		}
		model.APIKey = credential.AccessToken
		request.Body, err = request.GetBody()
		if err != nil {
			return Turn{}, err
		}
		setOpenAIAuth(request, model)
		setCodexHeaders(request, model)
		response, err = doModelRequest(request, model)
		if err != nil {
			return Turn{}, err
		}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusUnauthorized && model.CodexCredentialPath != "" {
			return Turn{}, fmt.Errorf("Codex authentication failed; reconnect OpenAI Codex in Settings: %w", modelHTTPError("OpenAI Codex", response))
		}
		return Turn{}, modelHTTPError("OpenAI Responses", response)
	}
	stream := io.Reader(response.Body)
	contentType := response.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/event-stream") {
		if contentType != "" {
			return Turn{}, modelHTTPError("OpenAI Responses (unexpected content type)", response)
		}
		reader := bufio.NewReader(response.Body)
		prefix, err := reader.Peek(6)
		if err != nil && err != io.EOF || !bytes.HasPrefix(prefix, []byte("event:")) && !bytes.HasPrefix(prefix, []byte("data:")) {
			response.Body = struct {
				io.Reader
				io.Closer
			}{reader, response.Body}
			return Turn{}, modelHTTPError("OpenAI Responses (unexpected content type)", response)
		}
		stream = reader
	}
	return parseResponsesStreamWithReasoning(stream, onText, onReasoning)
}

func parseResponses(body io.Reader) (Turn, error) {
	return parseResponsesStream(body, nil)
}

func parseResponsesStream(body io.Reader, onText func(string) error) (Turn, error) {
	return parseResponsesStreamWithReasoning(body, onText, nil)
}

func parseResponsesStreamWithReasoning(body io.Reader, onText, onReasoning func(string) error) (Turn, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), maxResponsesSnapshotEventBytes)
	var data strings.Builder
	sawData := false
	var text strings.Builder
	reasoningBytes := 0
	type reasoningKey struct {
		itemID string
		index  int
		raw    bool
	}
	reasoningText := map[reasoningKey]*strings.Builder{}
	// Terminal events repeat the full summary. Emit only the missing suffix
	// for each item/part, including gateways that never send summary deltas.
	emitReasoning := func(key reasoningKey, value string, snapshot bool) error {
		if value == "" {
			return nil
		}
		if key.itemID == "" || len(key.itemID) > 256 || key.index < 0 {
			return errors.New("invalid OpenAI Responses reasoning item")
		}
		previous := reasoningText[key]
		if previous == nil {
			previous = &strings.Builder{}
			reasoningText[key] = previous
		}
		if snapshot {
			if !strings.HasPrefix(value, previous.String()) {
				// Already published summaries cannot be replaced through a delta
				// callback. Preserve them if a gateway rewrites the final summary.
				return nil
			}
			value = value[previous.Len():]
		}
		if len(value) > maxReplyBytes-reasoningBytes {
			return errors.New("OpenAI Responses reasoning delta exceeds limits")
		}
		if value == "" {
			return nil
		}
		if onReasoning == nil {
			return errors.New("OpenAI Responses reasoning stream requires a sink")
		}
		if err := onReasoning(value); err != nil {
			return err
		}
		previous.WriteString(value)
		reasoningBytes += len(value)
		return nil
	}
	emitSummary := func(item responseItem) error {
		if item.Type != "reasoning" {
			return nil
		}
		for index, part := range item.Summary {
			if part.Type != "summary_text" {
				return errors.New("unsupported OpenAI Responses reasoning summary")
			}
			if err := emitReasoning(reasoningKey{itemID: item.ID, index: index}, part.Text, true); err != nil {
				return err
			}
		}
		return nil
	}
	calls := map[string]*responseCall{}
	var toolBytes toolCollectionBytes
	hostedItems := map[string]string{}
	hostedOutput := map[string]json.RawMessage{}
	var hostedOrder []string
	order := []string{}
	hadTextDelta := false
	completed := false
	var final []responseItem
	var finalRaw []json.RawMessage
	var usage *ModelUsage
	var sources citationSources
	var doneMessageText strings.Builder
	doneMessageIDs := map[string]bool{}
	addAnnotation := func(annotation responseAnnotation) error {
		if annotation.Type == "url_citation" {
			return sources.add(annotation.URL, "OpenAI Responses")
		}
		return nil
	}
	addAnnotationList := func(annotations []responseAnnotation) error {
		for _, annotation := range annotations {
			if err := addAnnotation(annotation); err != nil {
				return err
			}
		}
		return nil
	}
	consume := func(payload string) error {
		var event responseEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return fmt.Errorf("invalid OpenAI Responses event: %w", err)
		}
		if event.Item.ItemID != "" {
			if event.Item.ID != "" && event.Item.ID != event.Item.ItemID {
				return errors.New("conflicting OpenAI Responses output item IDs")
			}
			event.Item.ID = event.Item.ItemID
		} else if event.Type == "response.output_item.done" && event.Item.ID == "" {
			event.Item.ID = event.ItemID
		}
		snapshot := false
		switch event.Type {
		case "response.completed", "response.output_text.done", "response.refusal.done", "response.content_part.done", "response.reasoning_summary_text.done", "response.reasoning_text.done":
			snapshot = true
		case "response.output_item.done":
			snapshot = event.Item.Type == "message" || event.Item.Type == "reasoning"
		}
		if len(payload) > maxEventBytes && !snapshot {
			return errors.New("OpenAI Responses event exceeds 128 KiB")
		}
		switch event.Type {
		case "response.output_text.annotation.added":
			if event.ItemID == "" {
				return errors.New("OpenAI Responses annotation lacks item ID")
			}
			if err := addAnnotation(event.Annotation); err != nil {
				return err
			}
		case "response.output_text.delta", "response.refusal.delta":
			if event.ItemID == "" {
				return errors.New("OpenAI Responses text delta lacks item ID")
			}
			if text.Len()+len(event.Delta) > maxReplyBytes {
				return errors.New("OpenAI Responses reply exceeds 256 KiB")
			}
			text.WriteString(event.Delta)
			if onText != nil && event.Delta != "" {
				if err := onText(event.Delta); err != nil {
					return err
				}
			}
			if event.Delta != "" {
				hadTextDelta = true
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			key := reasoningKey{itemID: event.ItemID, index: event.SummaryIndex}
			if event.Type == "response.reasoning_text.delta" {
				key.index, key.raw = event.ContentIndex, true
			}
			return emitReasoning(key, event.Delta, false)
		case "response.reasoning_summary_text.done", "response.reasoning_text.done":
			key := reasoningKey{itemID: event.ItemID, index: event.SummaryIndex}
			if event.Type == "response.reasoning_text.done" {
				key.index, key.raw = event.ContentIndex, true
			}
			return emitReasoning(key, event.Text, true)
		case "response.output_item.added":
			if event.Item.Type != "function_call" {
				if _, supported := responseHostedTools[event.Item.Type]; supported {
					if event.Item.ID == "" || len(event.Item.ID) > 256 || hostedItems[event.Item.ID] != "" {
						return errors.New("invalid OpenAI Responses hosted tool item")
					}
					if err := toolBytes.set(event.Item.ID, len(event.Item.Type)); err != nil {
						return err
					}
					hostedItems[event.Item.ID] = event.Item.Type
				} else if event.Item.Type != "message" && event.Item.Type != "reasoning" {
					return errors.New("unsupported OpenAI Responses output item")
				}
				return nil
			}
			if event.Item.ID == "" || len(event.Item.ID) > 256 || len(event.Item.CallID) > 256 || len(event.Item.Name) > 128 || len(event.Item.Arguments) > maxToolArgsBytes {
				return errors.New("OpenAI Responses function call exceeds limits")
			}
			if calls[event.Item.ID] != nil {
				return errors.New("duplicate OpenAI Responses output item")
			}
			if err := toolBytes.set(event.Item.ID, len(event.Item.CallID)+len(event.Item.Name)+len(event.Item.Arguments)); err != nil {
				return err
			}
			calls[event.Item.ID] = &responseCall{item: event.Item}
			order = append(order, event.Item.ID)
		case "response.function_call_arguments.delta":
			call := calls[event.ItemID]
			if call == nil || call.args.Len()+len(event.Delta) > maxToolArgsBytes {
				return errors.New("OpenAI Responses function arguments exceed limits")
			}
			call.args.WriteString(event.Delta)
			if err := toolBytes.set(event.ItemID, len(call.item.CallID)+len(call.item.Name)+len(call.item.Arguments)+call.args.Len()); err != nil {
				return err
			}
		case "response.function_call_arguments.done":
			call := calls[event.ItemID]
			if call == nil || len(event.Arguments) > maxToolArgsBytes || (call.args.Len() > 0 && call.args.String() != event.Arguments) {
				return errors.New("OpenAI Responses function arguments disagree")
			}
			if call.args.Len() == 0 {
				call.args.WriteString(event.Arguments)
			}
			if err := toolBytes.set(event.ItemID, len(call.item.CallID)+len(call.item.Name)+len(call.item.Arguments)+call.args.Len()); err != nil {
				return err
			}
		case "response.output_item.done":
			if event.Item.Type == "reasoning" {
				return emitSummary(event.Item)
			}
			if event.Item.Type != "function_call" {
				if event.Item.Type == "message" {
					messageID := event.Item.ID
					if messageID == "" {
						// A few Responses gateways omit the message ID on done
						// items. This key is local to this stream and is not used
						// for provider continuation.
						messageID = fmt.Sprintf("message-%d", len(doneMessageIDs)+1)
					}
					if len(messageID) > 256 || doneMessageIDs[messageID] {
						return errors.New("invalid OpenAI Responses message item")
					}
					doneMessageIDs[messageID] = true
					for _, part := range event.Item.Content {
						if err := addAnnotationList(part.Annotations); err != nil {
							return err
						}
						if part.Type == "output_text" {
							if doneMessageText.Len()+len(part.Text) > maxReplyBytes {
								return errors.New("OpenAI Responses reply exceeds 256 KiB")
							}
							doneMessageText.WriteString(part.Text)
						} else if part.Type == "refusal" {
							if doneMessageText.Len()+len(part.Refusal) > maxReplyBytes {
								return errors.New("OpenAI Responses reply exceeds 256 KiB")
							}
							doneMessageText.WriteString(part.Refusal)
						}
					}
					return nil
				}
				if _, supported := responseHostedTools[event.Item.Type]; supported {
					if event.Item.ID == "" || len(event.Item.ID) > 256 || hostedItems[event.Item.ID] != "" && hostedItems[event.Item.ID] != event.Item.Type {
						return errors.New("invalid OpenAI Responses hosted tool item")
					}
					var envelope struct {
						Item map[string]json.RawMessage `json:"item"`
					}
					if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
						return err
					}
					if envelope.Item == nil {
						envelope.Item = map[string]json.RawMessage{}
					}
					envelope.Item["id"], _ = json.Marshal(event.Item.ID)
					delete(envelope.Item, "item_id")
					raw, err := json.Marshal(envelope.Item)
					if err != nil || len(raw) > maxHostedResultBytes {
						return errors.New("OpenAI Responses hosted tool result exceeds limits")
					}
					if previous, ok := hostedOutput[event.Item.ID]; ok {
						if !bytes.Equal(previous, raw) {
							return errors.New("inconsistent OpenAI Responses hosted tool result")
						}
					} else {
						hostedOrder = append(hostedOrder, event.Item.ID)
					}
					hostedOutput[event.Item.ID] = raw
					if err := toolBytes.set(event.Item.ID, len(raw)); err != nil {
						return err
					}
					hostedItems[event.Item.ID] = event.Item.Type
				} else if event.Item.Type != "message" && event.Item.Type != "reasoning" {
					return errors.New("unsupported OpenAI Responses output item")
				}
				return nil
			}
			call := calls[event.Item.ID]
			if call == nil || event.Item.CallID == "" || event.Item.Name == "" || (call.item.CallID != "" && call.item.CallID != event.Item.CallID) || (call.item.Name != "" && call.item.Name != event.Item.Name) || len(event.Item.Arguments) > maxToolArgsBytes || (call.args.Len() > 0 && call.args.String() != event.Item.Arguments) {
				return errors.New("OpenAI Responses function item is incomplete or inconsistent")
			}
			call.item = event.Item
			if err := toolBytes.set(event.Item.ID, len(call.item.CallID)+len(call.item.Name)+len(call.item.Arguments)+call.args.Len()); err != nil {
				return err
			}
		case "response.completed":
			if event.Response.Status != "completed" {
				return errors.New("OpenAI Responses completed with unexpected status")
			}
			if event.Response.Usage != nil {
				candidate := ModelUsage{InputTokens: event.Response.Usage.InputTokens, OutputTokens: event.Response.Usage.OutputTokens, CacheReadTokens: event.Response.Usage.InputDetails.CachedTokens, ReasoningTokens: event.Response.Usage.OutputDetails.ReasoningTokens}
				if !validModelUsage(candidate) {
					return errors.New("OpenAI Responses reported invalid token usage")
				}
				usage = &candidate
			}
			final = final[:0]
			for index, raw := range event.Response.Output {
				var item responseItem
				if err := json.Unmarshal(raw, &item); err != nil {
					return fmt.Errorf("invalid OpenAI Responses output item: %w", err)
				}
				if item.ItemID != "" {
					if item.ID != "" && item.ID != item.ItemID {
						return errors.New("conflicting OpenAI Responses completed item IDs")
					}
					item.ID = item.ItemID
					var fields map[string]json.RawMessage
					if err := json.Unmarshal(raw, &fields); err != nil {
						return fmt.Errorf("invalid OpenAI Responses output item: %w", err)
					}
					fields["id"] = fields["item_id"]
					delete(fields, "item_id")
					canonical, err := json.Marshal(fields)
					if err != nil {
						return fmt.Errorf("invalid OpenAI Responses output item: %w", err)
					}
					event.Response.Output[index] = canonical
				}
				if err := emitSummary(item); err != nil {
					return err
				}
				final = append(final, item)
			}
			finalRaw = event.Response.Output
			completed = true
		case "response.failed", "error":
			return modelStreamError("OpenAI Responses", []byte(payload))
		case "response.incomplete":
			if err := contextStopError("OpenAI Responses", event.Response.IncompleteDetails.Reason); err != nil {
				return err
			}
			switch event.Response.IncompleteDetails.Reason {
			case "max_output_tokens", "content_filter":
				return fmt.Errorf("OpenAI Responses incomplete: %s", event.Response.IncompleteDetails.Reason)
			}
			return errors.New("OpenAI Responses incomplete")
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			sawData = false
			if data.Len() == 0 {
				continue
			}
			payload := data.String()
			data.Reset()
			if sseKeepalive(payload) {
				continue
			}
			if err := consume(payload); err != nil {
				return Turn{}, err
			}
			if completed {
				break
			}
			continue
		}
		if fragment, ok := sseDataLine(line); ok {
			separator := 0
			if sawData {
				separator = 1
			}
			if data.Len()+len(fragment)+separator > maxResponsesSnapshotEventBytes {
				return Turn{}, errors.New("OpenAI Responses event exceeds 2 MiB")
			}
			if separator != 0 {
				data.WriteByte('\n')
			}
			data.WriteString(fragment)
			sawData = true
		}
	}
	if err := scanner.Err(); err != nil {
		return Turn{}, fmt.Errorf("OpenAI Responses stream read: %w", err)
	}
	if !completed {
		return Turn{}, errors.New("OpenAI Responses stream ended before completion")
	}
	var completedText strings.Builder
	hasCompletedText := false
	for _, item := range final {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			for _, annotation := range part.Annotations {
				if err := addAnnotation(annotation); err != nil {
					return Turn{}, err
				}
			}
			value := part.Text
			if part.Type == "refusal" {
				value = part.Refusal
			}
			if part.Type == "output_text" || part.Type == "refusal" {
				hasCompletedText = true
				if completedText.Len()+len(value) > maxReplyBytes {
					return Turn{}, errors.New("OpenAI Responses reply exceeds 256 KiB")
				}
				completedText.WriteString(value)
			}
		}
	}
	if hadTextDelta && hasCompletedText && completedText.String() != text.String() {
		return Turn{}, errors.New("OpenAI Responses completed text disagrees with streamed text")
	}
	if doneMessageText.Len() > 0 {
		if hadTextDelta && doneMessageText.String() != text.String() {
			return Turn{}, errors.New("OpenAI Responses output item text disagrees with streamed text")
		}
		if hasCompletedText && doneMessageText.String() != completedText.String() {
			return Turn{}, errors.New("OpenAI Responses output item text disagrees with completed text")
		}
	}
	if !hadTextDelta && hasCompletedText {
		text.WriteString(completedText.String())
		if onText != nil && completedText.Len() > 0 {
			if err := onText(completedText.String()); err != nil {
				return Turn{}, err
			}
		}
	} else if !hadTextDelta && !hasCompletedText && doneMessageText.Len() > 0 {
		text.WriteString(doneMessageText.String())
		if onText != nil {
			if err := onText(doneMessageText.String()); err != nil {
				return Turn{}, err
			}
		}
	}
	finalCallIDs := make(map[string]bool)
	for _, item := range final {
		if item.Type != "function_call" {
			continue
		}
		finalCallIDs[item.ID] = true
		if item.ID == "" || item.CallID == "" || item.Name == "" || len(item.CallID) > 256 || len(item.Name) > 128 || len(item.Arguments) > maxToolArgsBytes {
			return Turn{}, errors.New("OpenAI Responses function call exceeds limits")
		}
		if call := calls[item.ID]; call != nil {
			if (call.item.CallID != "" && call.item.CallID != item.CallID) || (call.item.Name != "" && call.item.Name != item.Name) || (call.args.Len() > 0 && call.args.String() != item.Arguments) {
				return Turn{}, errors.New("OpenAI Responses final function call is inconsistent")
			}
			call.item = item
			if err := toolBytes.set(item.ID, len(call.item.CallID)+len(call.item.Name)+len(call.item.Arguments)+call.args.Len()); err != nil {
				return Turn{}, err
			}
			continue
		}
		if err := toolBytes.set(item.ID, len(item.CallID)+len(item.Name)+len(item.Arguments)); err != nil {
			return Turn{}, err
		}
		calls[item.ID] = &responseCall{item: item}
		order = append(order, item.ID)
	}
	if len(finalRaw) > 0 {
		for id := range calls {
			if !finalCallIDs[id] {
				return Turn{}, errors.New("OpenAI Responses completed output omits a function call")
			}
		}
	}
	if len(finalRaw) == 0 && len(calls) == 0 {
		// Some Responses gateways omit the completed output array entirely.
		// Retain self-contained output_item.done results. Mixed local-tool
		// continuations still require a complete final output for replay.
		// A started call or partially populated final array is not sufficient.
		for _, id := range hostedOrder {
			raw := hostedOutput[id]
			var item responseItem
			if json.Unmarshal(raw, &item) != nil || (item.Status != "completed" && item.Status != "failed") {
				continue // The identity check below rejects this unfinished item.
			}
			final, finalRaw = append(final, item), append(finalRaw, raw)
		}
	}
	for id, kind := range hostedItems {
		found := false
		for _, item := range final {
			if item.ID == id && item.Type == kind {
				found = true
				break
			}
		}
		if !found {
			return Turn{}, errors.New("OpenAI Responses completed output omits a hosted tool item")
		}
	}
	outputLimit := maxResponsesSnapshotEventBytes
	if len(calls) > 0 {
		outputLimit = maxResponseOutputBytes
	}
	if !validResponseOutput(finalRaw, outputLimit, len(calls) > 0) {
		return Turn{}, errors.New("OpenAI Responses output items exceed limits or contain unsupported types")
	}
	turn := Turn{Text: text.String(), Sources: sources.urls, FinishReason: "completed", Usage: usage}
	hostedCalls, err := responseHostedCalls(finalRaw)
	if err != nil {
		return Turn{}, err
	}
	turn.HostedCalls = hostedCalls
	if len(calls) > 0 || len(hostedCalls) > 0 && strings.TrimSpace(turn.Text) == "" {
		if !validResponseOutput(finalRaw, maxResponseOutputBytes, true) {
			return Turn{}, errors.New("OpenAI Responses continuation output exceeds limits")
		}
		reasoningItems, err := responseReasoningItems(final)
		if err != nil {
			return Turn{}, err
		}
		turn.ResponseReasoning = reasoningItems
		turn.ResponseOutput = finalRaw
	}
	for _, id := range order {
		call := calls[id]
		args := call.item.Arguments
		if args == "" {
			args = call.args.String()
		}
		var arguments map[string]any
		if call.item.CallID == "" || call.item.Name == "" || len(args) > maxToolArgsBytes || json.Unmarshal([]byte(args), &arguments) != nil || arguments == nil {
			return Turn{}, errors.New("OpenAI Responses returned incomplete function arguments")
		}
		var tool ToolCall
		tool.ID, tool.Type, tool.Function.Name, tool.Function.Arguments = call.item.CallID, "function", call.item.Name, args
		turn.Calls = append(turn.Calls, tool)
	}
	return turn, nil
}
