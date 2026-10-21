package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/auth/bearer"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	modelcfg "vista/internal/model"
)

func bedrockTurnWithReasoning(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any, onText, onReasoning func(string) error) (Turn, error) {
	if err := validateImageMessages(messages); err != nil {
		return Turn{}, err
	}
	if model.ID == "" || model.AWSRegion == "" || model.APIKey == "" && (model.AWSAccessID == "" || model.AWSSecret == "") {
		return Turn{}, errors.New("Bedrock model, region, and credentials are required")
	}
	cfg := aws.Config{Region: model.AWSRegion, HTTPClient: bedrockHTTPClient}
	// Keep the AWS SDK identity unless this connection explicitly overrides it.
	for name, value := range model.Headers {
		if strings.EqualFold(name, "User-Agent") {
			cfg.APIOptions = append(cfg.APIOptions, smithyhttp.SetHeaderValue("User-Agent", value))
		}
	}
	if model.APIKey != "" {
		cfg.BearerAuthTokenProvider = bearer.StaticTokenProvider{Token: bearer.Token{Value: model.APIKey}}
		cfg.AuthSchemePreference = []string{"httpBearerAuth"}
	} else {
		cfg.Credentials = credentials.NewStaticCredentialsProvider(model.AWSAccessID, model.AWSSecret, model.AWSSession)
	}
	if model.BaseURL != "" {
		u, err := url.Parse(model.BaseURL)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) {
			return Turn{}, errors.New("invalid Bedrock base URL: HTTPS or loopback HTTP required")
		}
		cfg.BaseEndpoint = aws.String(strings.TrimRight(u.String(), "/"))
	}
	input := &bedrockruntime.ConverseStreamInput{ModelId: aws.String(bedrockWireModelID(model.ID, model.AWSRegion))}
	additional := map[string]any{}
	if !model.ReasoningDisabled && model.Thinking != nil {
		requested := model.Thinking.BudgetTokens
		if requested == nil {
			requested = model.Thinking.BudgetAlias
		}
		if model.Thinking.Type != "adaptive" && (model.Thinking.Type != "enabled" || requested == nil || *requested < 1024 || *requested > 32768) {
			return Turn{}, errors.New("invalid Bedrock thinking option")
		}
		if !strings.Contains(model.ID, "anthropic.claude") {
			return Turn{}, errors.New("Bedrock thinking option requires an Anthropic Claude model")
		}
		maxTokens := model.OutputLimit
		if maxTokens <= 0 {
			maxTokens = 4096
		}
		maxTokens = min(maxTokens, 64000)
		if maxTokens < 2048 {
			return Turn{}, errors.New("Bedrock output limit cannot fit thinking budget")
		}
		input.InferenceConfig = &types.InferenceConfiguration{MaxTokens: aws.Int32(int32(maxTokens))}
		if model.Thinking.Type == "adaptive" {
			additional["thinking"] = map[string]any{"type": "adaptive"}
		} else {
			additional["thinking"] = map[string]any{"type": "enabled", "budget_tokens": min(*requested, maxTokens/2)}
		}
	}
	if !model.ReasoningDisabled && model.ReasoningEffort != "" {
		if model.ProviderID != "amazon-bedrock" || !validBedrockEffort(model.ReasoningEffort) {
			return Turn{}, errors.New("invalid Bedrock reasoning effort")
		}
		switch {
		case strings.Contains(model.ID, "anthropic."):
			additional["output_config"] = map[string]any{"effort": model.ReasoningEffort}
		case strings.Contains(model.ID, "openai.gpt-oss"):
			additional["reasoning_effort"] = model.ReasoningEffort
		case strings.Contains(model.ID, "openai."):
			additional["reasoning"] = map[string]any{"effort": model.ReasoningEffort}
		default:
			additional["reasoningConfig"] = map[string]any{"maxReasoningEffort": model.ReasoningEffort}
		}
	}
	if len(additional) > 0 {
		input.AdditionalModelRequestFields = document.NewLazyDocument(additional)
	}
	for _, message := range messages {
		switch message.Role {
		case "system":
			input.System = append(input.System, &types.SystemContentBlockMemberText{Value: message.Content})
		case "user", "assistant", "tool":
			var item types.Message
			switch message.Role {
			case "user":
				item.Role = types.ConversationRoleUser
				if message.Content != "" || len(message.Images) == 0 {
					item.Content = append(item.Content, &types.ContentBlockMemberText{Value: message.Content})
				}
				for _, image := range message.Images {
					item.Content = append(item.Content, &types.ContentBlockMemberImage{Value: types.ImageBlock{
						Format: types.ImageFormat(strings.TrimPrefix(image.MediaType, "image/")),
						Source: &types.ImageSourceMemberBytes{Value: image.Data},
					}})
				}
			case "assistant":
				item.Role = types.ConversationRoleAssistant
				if len(message.BedrockParts) > 0 {
					parts, err := bedrockReplayParts(message.BedrockParts)
					if err != nil {
						return Turn{}, err
					}
					item.Content = append(item.Content, parts...)
				} else {
					if message.Content != "" {
						item.Content = append(item.Content, &types.ContentBlockMemberText{Value: message.Content})
					}
					for _, call := range message.ToolCalls {
						if call.ID == "" || call.Function.Name == "" || len(call.Function.Arguments) > maxToolArgsBytes {
							return Turn{}, errors.New("invalid Bedrock previous tool call")
						}
						var args map[string]any
						if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil || args == nil {
							return Turn{}, errors.New("invalid Bedrock previous tool arguments")
						}
						item.Content = append(item.Content, &types.ContentBlockMemberToolUse{Value: types.ToolUseBlock{ToolUseId: aws.String(call.ID), Name: aws.String(call.Function.Name), Input: document.NewLazyDocument(args)}})
					}
				}
			case "tool":
				if message.ToolCallID == "" {
					return Turn{}, errors.New("Bedrock tool result lacks a call ID")
				}
				item.Role = types.ConversationRoleUser
				item.Content = []types.ContentBlock{&types.ContentBlockMemberToolResult{Value: types.ToolResultBlock{ToolUseId: aws.String(message.ToolCallID), Content: []types.ToolResultContentBlock{&types.ToolResultContentBlockMemberText{Value: message.Content}}}}}
			}
			if len(item.Content) > 0 {
				input.Messages = append(input.Messages, item)
			}
		default:
			return Turn{}, errors.New("invalid Bedrock message role")
		}
	}
	if len(schemas) > 0 {
		config := &types.ToolConfiguration{Tools: make([]types.Tool, 0, len(schemas))}
		for _, schema := range schemas {
			function, ok := schema["function"].(map[string]any)
			name, named := function["name"].(string)
			parameters, paramsOK := function["parameters"].(map[string]any)
			if schema["type"] != "function" || !ok || !named || name == "" || !paramsOK {
				return Turn{}, errors.New("invalid Bedrock function schema")
			}
			_, err := json.Marshal(parameters)
			if err != nil {
				return Turn{}, err
			}
			spec := types.ToolSpecification{Name: aws.String(name), InputSchema: &types.ToolInputSchemaMemberJson{Value: document.NewLazyDocument(parameters)}}
			if description, ok := function["description"].(string); ok && description != "" {
				spec.Description = aws.String(description)
			}
			config.Tools = append(config.Tools, &types.ToolMemberToolSpec{Value: spec})
		}
		input.ToolConfig = config
	}
	output, err := bedrockruntime.NewFromConfig(cfg).ConverseStream(ctx, input)
	if err != nil {
		return Turn{}, bedrockModelError(err)
	}
	stream := output.GetStream()
	defer stream.Close()
	return parseBedrockEventsWithReasoning(stream.Events(), stream.Err, onText, onReasoning)
}

func validBedrockEffort(value string) bool {
	switch value {
	case "low", "medium", "high", "xhigh", "max":
		return true
	}
	return false
}

func bedrockReplayParts(parts []BedrockPart) ([]types.ContentBlock, error) {
	nonToolBlocks := 0
	content := make([]types.ContentBlock, 0, len(parts))
	for _, part := range parts {
		if part.Kind != "tool" {
			nonToolBlocks++
			if nonToolBlocks > 64 {
				return nil, errors.New("Bedrock previous content has too many blocks")
			}
		}
		switch part.Kind {
		case "text":
			content = append(content, &types.ContentBlockMemberText{Value: part.Text})
		case "reasoning":
			if len(part.Text) > 64<<10 || len(part.Signature) > 16<<10 || len(part.Redacted) > 64<<10 {
				return nil, errors.New("Bedrock previous reasoning exceeds limits")
			}
			if part.Signature == "" && len(part.Redacted) == 0 {
				// Unsigned reasoning from an interrupted turn cannot be replayed
				// as a signed Bedrock block or exposed as user-facing text.
				continue
			}
			if part.Signature != "" && part.Text != "" && len(part.Redacted) == 0 {
				content = append(content, &types.ContentBlockMemberReasoningContent{Value: &types.ReasoningContentBlockMemberReasoningText{Value: types.ReasoningTextBlock{Text: aws.String(part.Text), Signature: aws.String(part.Signature)}}})
			} else if len(part.Redacted) > 0 && part.Signature == "" && part.Text == "" {
				content = append(content, &types.ContentBlockMemberReasoningContent{Value: &types.ReasoningContentBlockMemberRedactedContent{Value: part.Redacted}})
			} else {
				return nil, errors.New("invalid Bedrock previous reasoning block")
			}
		case "tool":
			call := part.Call
			if call.ID == "" || call.Function.Name == "" || len(call.Function.Arguments) > maxToolArgsBytes {
				return nil, errors.New("invalid Bedrock previous tool call")
			}
			var args map[string]any
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil || args == nil {
				return nil, errors.New("invalid Bedrock previous tool arguments")
			}
			content = append(content, &types.ContentBlockMemberToolUse{Value: types.ToolUseBlock{ToolUseId: aws.String(call.ID), Name: aws.String(call.Function.Name), Input: document.NewLazyDocument(args)}})
		default:
			return nil, errors.New("invalid Bedrock previous content block")
		}
	}
	return content, nil
}

func bedrockWireModelID(id, region string) string {
	if strings.HasPrefix(id, "arn:") {
		return id
	}
	for _, prefix := range []string{"global.", "us.", "eu.", "jp.", "apac.", "au."} {
		if strings.HasPrefix(id, prefix) {
			return id
		}
	}
	contains := func(parts ...string) bool {
		for _, part := range parts {
			if strings.Contains(id, part) {
				return true
			}
		}
		return false
	}
	switch {
	case strings.HasPrefix(region, "us-") && !strings.HasPrefix(region, "us-gov") && contains("nova-micro", "nova-lite", "nova-pro", "nova-premier", "nova-2", "claude", "deepseek.r1"):
		return "us." + id
	case (region == "eu-west-1" || region == "eu-west-2" || region == "eu-west-3" || region == "eu-north-1" || region == "eu-central-1" || region == "eu-south-1" || region == "eu-south-2") && contains("claude", "nova-lite", "nova-micro", "llama3", "pixtral"):
		return "eu." + id
	case (region == "ap-southeast-2" || region == "ap-southeast-4") && contains("anthropic.claude-sonnet-4-5", "anthropic.claude-haiku"):
		return "au." + id
	case region == "ap-northeast-1" && contains("claude", "nova-lite", "nova-micro", "nova-pro"):
		return "jp." + id
	case strings.HasPrefix(region, "ap-") && contains("claude", "nova-lite", "nova-micro", "nova-pro"):
		return "apac." + id
	}
	return id
}

func parseBedrockEvents(events <-chan types.ConverseStreamOutput, streamErr func() error, onText func(string) error) (Turn, error) {
	return parseBedrockEventsWithReasoning(events, streamErr, onText, nil)
}

func parseBedrockEventsWithReasoning(events <-chan types.ConverseStreamOutput, streamErr func() error, onText, onReasoning func(string) error) (Turn, error) {
	var reply strings.Builder
	var calls []ToolCall
	var toolBytes toolCollectionBytes
	pending := map[int32]*ToolCall{}
	parts := map[int32]BedrockPart{}
	closed := map[int32]bool{}
	open := map[int32]bool{}
	toolIDs := map[string]bool{}
	nonToolBlocks := 0
	reasoningBytes := 0
	redactedBytes := 0
	finished := false
	finishReason := ""
	var usage *ModelUsage
	readMetadata := func(metadata types.ConverseStreamMetadataEvent) error {
		if metadata.Usage == nil || metadata.Usage.InputTokens == nil || metadata.Usage.OutputTokens == nil {
			return errors.New("Bedrock metadata lacks token usage")
		}
		wire := metadata.Usage
		candidate := ModelUsage{InputTokens: int64(*wire.InputTokens), OutputTokens: int64(*wire.OutputTokens)}
		if wire.CacheReadInputTokens != nil {
			candidate.CacheReadTokens = int64(*wire.CacheReadInputTokens)
		}
		if !validModelUsage(candidate) || wire.TotalTokens != nil && !validReportedTokens(int64(*wire.TotalTokens)) || wire.CacheWriteInputTokens != nil && !validReportedTokens(int64(*wire.CacheWriteInputTokens)) {
			return errors.New("Bedrock reported invalid token usage")
		}
		usage = &candidate
		return nil
	}
	for event := range events {
		if finished {
			if metadata, ok := event.(*types.ConverseStreamOutputMemberMetadata); ok {
				if err := readMetadata(metadata.Value); err != nil {
					return Turn{}, err
				}
				continue
			}
			return Turn{}, errors.New("Bedrock stream continued after message stop")
		}
		switch value := event.(type) {
		case *types.ConverseStreamOutputMemberMessageStart:
		case *types.ConverseStreamOutputMemberMetadata:
			if err := readMetadata(value.Value); err != nil {
				return Turn{}, err
			}
		case *types.ConverseStreamOutputMemberContentBlockStart:
			start, ok := value.Value.Start.(*types.ContentBlockStartMemberToolUse)
			if !ok || value.Value.ContentBlockIndex == nil || *value.Value.ContentBlockIndex < 0 || start.Value.ToolUseId == nil || start.Value.Name == nil {
				return Turn{}, errors.New("unsupported or invalid Bedrock content block start")
			}
			if closed[*value.Value.ContentBlockIndex] || parts[*value.Value.ContentBlockIndex].Kind != "" {
				return Turn{}, errors.New("duplicate Bedrock content block start")
			}
			id, name := *start.Value.ToolUseId, *start.Value.Name
			if id == "" || name == "" || len(id) > 256 || len(name) > 128 || pending[*value.Value.ContentBlockIndex] != nil {
				return Turn{}, errors.New("invalid Bedrock tool call start")
			}
			if toolIDs[id] {
				return Turn{}, errors.New("duplicate Bedrock tool call ID")
			}
			if err := toolBytes.set(fmt.Sprint(*value.Value.ContentBlockIndex), len(id)+len(name)); err != nil {
				return Turn{}, err
			}
			toolIDs[id] = true
			open[*value.Value.ContentBlockIndex] = true
			pending[*value.Value.ContentBlockIndex] = &ToolCall{ID: id, Type: "function"}
			pending[*value.Value.ContentBlockIndex].Function.Name = name
		case *types.ConverseStreamOutputMemberContentBlockDelta:
			index := value.Value.ContentBlockIndex
			if index == nil || *index < 0 || closed[*index] {
				return Turn{}, errors.New("Bedrock content block index is missing")
			}
			if !open[*index] {
				if nonToolBlocks >= 64 {
					return Turn{}, errors.New("Bedrock content blocks exceed limits")
				}
				nonToolBlocks++
			}
			open[*index] = true
			switch delta := value.Value.Delta.(type) {
			case *types.ContentBlockDeltaMemberText:
				part := parts[*index]
				if part.Kind != "" && part.Kind != "text" || pending[*index] != nil {
					return Turn{}, errors.New("Bedrock mixed content block types")
				}
				if reply.Len()+len(delta.Value) > maxReplyBytes {
					return Turn{}, errors.New("Bedrock text exceeds 256 KiB")
				}
				reply.WriteString(delta.Value)
				part.Kind = "text"
				part.Text += delta.Value
				parts[*index] = part
				if onText != nil && delta.Value != "" {
					if err := onText(delta.Value); err != nil {
						return Turn{}, err
					}
				}
			case *types.ContentBlockDeltaMemberToolUse:
				call := pending[*index]
				if call == nil || delta.Value.Input == nil || len(call.Function.Arguments)+len(*delta.Value.Input) > maxToolArgsBytes {
					return Turn{}, errors.New("Bedrock tool arguments exceed limits")
				}
				call.Function.Arguments += *delta.Value.Input
				if err := toolBytes.set(fmt.Sprint(*index), len(call.ID)+len(call.Function.Name)+len(call.Function.Arguments)); err != nil {
					return Turn{}, err
				}
			case *types.ContentBlockDeltaMemberReasoningContent:
				part := parts[*index]
				if part.Kind != "" && part.Kind != "reasoning" || pending[*index] != nil {
					return Turn{}, errors.New("Bedrock mixed content block types")
				}
				part.Kind = "reasoning"
				switch reasoning := delta.Value.(type) {
				case *types.ReasoningContentBlockDeltaMemberText:
					if len(part.Redacted) > 0 || reasoningBytes+len(reasoning.Value) > 64<<10 {
						return Turn{}, errors.New("Bedrock reasoning exceeds 64 KiB")
					}
					reasoningBytes += len(reasoning.Value)
					part.Text += reasoning.Value
					if onReasoning != nil && reasoning.Value != "" {
						if err := onReasoning(reasoning.Value); err != nil {
							return Turn{}, err
						}
					}
				case *types.ReasoningContentBlockDeltaMemberSignature:
					if len(part.Redacted) > 0 || len(part.Signature)+len(reasoning.Value) > 16<<10 {
						return Turn{}, errors.New("Bedrock reasoning signature exceeds limits")
					}
					part.Signature += reasoning.Value
				case *types.ReasoningContentBlockDeltaMemberRedactedContent:
					if part.Text != "" || part.Signature != "" || redactedBytes+len(reasoning.Value) > 64<<10 {
						return Turn{}, errors.New("Bedrock redacted reasoning exceeds limits")
					}
					redactedBytes += len(reasoning.Value)
					part.Redacted = append(part.Redacted, reasoning.Value...)
				default:
					return Turn{}, errors.New("unsupported Bedrock reasoning delta")
				}
				parts[*index] = part
			default:
				return Turn{}, errors.New("unsupported Bedrock content delta")
			}
		case *types.ConverseStreamOutputMemberContentBlockStop:
			if value.Value.ContentBlockIndex == nil || *value.Value.ContentBlockIndex < 0 {
				return Turn{}, errors.New("Bedrock content block index is missing")
			}
			index := *value.Value.ContentBlockIndex
			if closed[index] {
				return Turn{}, errors.New("duplicate Bedrock content block stop")
			}
			if !open[index] || parts[index].Kind == "" && pending[index] == nil {
				return Turn{}, errors.New("Bedrock content block stopped without content")
			}
			closed[index] = true
			if call := pending[index]; call != nil {
				var args map[string]any
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil || args == nil {
					return Turn{}, errors.New("invalid Bedrock tool arguments")
				}
				calls = append(calls, *call)
				parts[index] = BedrockPart{Kind: "tool", Call: *call}
				delete(pending, index)
			}
		case *types.ConverseStreamOutputMemberMessageStop:
			if len(pending) != 0 || len(closed) > len(open) || value.Value.StopReason != types.StopReasonEndTurn && value.Value.StopReason != types.StopReasonStopSequence && value.Value.StopReason != types.StopReasonToolUse || value.Value.StopReason == types.StopReasonToolUse && len(calls) == 0 {
				return Turn{}, errors.New("Bedrock stream ended with incomplete output")
			}
			finished = true
			finishReason = string(value.Value.StopReason)
		default:
			return Turn{}, errors.New("unknown Bedrock stream event")
		}
	}
	if err := streamErr(); err != nil {
		return Turn{}, bedrockModelError(err)
	}
	if !finished {
		return Turn{}, errors.New("Bedrock stream ended without message stop")
	}
	indices := make([]int, 0, len(parts))
	for index := range parts {
		indices = append(indices, int(index))
	}
	sort.Ints(indices)
	ordered := make([]BedrockPart, 0, len(indices))
	for _, index := range indices {
		ordered = append(ordered, parts[int32(index)])
	}
	return Turn{Text: reply.String(), Calls: calls, BedrockParts: ordered, FinishReason: finishReason, Usage: usage}, nil
}

func bedrockModelError(err error) error {
	var apiError smithy.APIError
	if errors.As(err, &apiError) && (apiError.ErrorCode() == "context_length_exceeded" || apiError.ErrorCode() == "ValidationException" && overflowText(apiError.ErrorMessage())) {
		return &ModelHTTPError{Provider: "Bedrock", Code: "context_length_exceeded"}
	}
	return fmt.Errorf("Bedrock: %w", err)
}
