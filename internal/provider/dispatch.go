package provider

import (
	"context"
	"fmt"

	modelcfg "vista/internal/model"
)

type protocolAdapter struct {
	turn               func(context.Context, modelcfg.Model, []ChatMessage, []map[string]any, func(string) error, func(string) error) (Turn, error)
	imageInput         bool
	ambientCredentials bool
	applyReasoning     func(*modelcfg.Model, string)
	prepareRun         func(context.Context, modelcfg.Model, string) (modelcfg.Model, string, error)
}

// Protocol routing and input capabilities share one static definition. Model
// catalog capabilities may further restrict what a particular model accepts.
var protocolAdapters = map[string]protocolAdapter{
	"openai-chat":              {turn: openAIChatTurnWithReasoning, imageInput: true, applyReasoning: applyChatReasoning},
	"watsonx":                  {turn: openAIChatTurnWithReasoning, imageInput: true},
	"sap-ai-core":              {turn: openAIChatTurnWithReasoning, imageInput: true},
	"openai-responses":         {turn: openAIResponsesTurnWithReasoning, imageInput: true, applyReasoning: applyResponsesReasoning},
	"anthropic":                {turn: anthropicTurn, imageInput: true, applyReasoning: applyAnthropicReasoning},
	"vertex-anthropic":         {turn: anthropicTurn, imageInput: true, applyReasoning: applyAnthropicReasoning},
	"gemini":                   {turn: geminiTurnWithReasoning, imageInput: true, applyReasoning: applyGeminiReasoning},
	"vertex-gemini":            {turn: geminiTurnWithReasoning, imageInput: true, applyReasoning: applyGeminiReasoning},
	"mistral":                  {turn: mistralTurnWithReasoning, imageInput: true},
	"vercel-gateway":           {turn: vercelGatewayTurn, imageInput: true},
	"cohere":                   {turn: cohereTurnWithReasoning, imageInput: true},
	"bedrock":                  {turn: bedrockTurnWithReasoning, imageInput: true, prepareRun: resolveBedrockForRun, ambientCredentials: true, applyReasoning: applyBedrockReasoning},
	"bedrock-mantle-chat":      {turn: bedrockMantleTurn, prepareRun: resolveBedrockForRun, ambientCredentials: true, applyReasoning: applyChatReasoning},
	"bedrock-mantle-responses": {turn: bedrockMantleTurn, imageInput: true, prepareRun: resolveBedrockForRun, ambientCredentials: true, applyReasoning: applyResponsesReasoning},
}

// SupportsProtocol reports whether a protocol has a model adapter.
func SupportsProtocol(protocol string) bool {
	_, ok := protocolAdapters[protocol]
	return ok
}

// SupportsImageInput reports protocol support, independently of model-specific
// input capabilities. Unknown protocols do not accept images.
func SupportsImageInput(protocol string) bool {
	return protocolAdapters[protocol].imageInput
}

// SupportedInputModalities combines model metadata with the installed protocol
// adapter. Consumers of public model metadata need no protocol whitelist.
func SupportedInputModalities(protocol string, declared []string) []string {
	if !SupportsProtocol(protocol) {
		return nil
	}
	var supported []string
	for _, modality := range declared {
		if modality == "text" || modality == "image" && SupportsImageInput(protocol) {
			supported = append(supported, modality)
		}
	}
	return supported
}

func ModelTurn(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any) (Turn, error) {
	return ModelTurnStream(ctx, model, messages, schemas, nil)
}

// ModelTurnStream reports bounded text fragments as they arrive. The caller
// must treat them as provisional until the turn completes without error.
func ModelTurnStream(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any, onText func(string) error) (Turn, error) {
	return ModelTurnStreamWithReasoning(ctx, model, messages, schemas, onText, nil)
}

func ModelTurnStreamWithReasoning(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any, onText, onReasoning func(string) error) (Turn, error) {
	messages = prepareRequestMessages(ctx, messages)
	messages = projectToolImages(messages)
	if err := validateImageMessages(messages); err != nil {
		return Turn{}, err
	}
	if model.Custom {
		if !IsCustomProtocol(model.Provider) || !validCustomBaseURL(model.BaseURL) {
			return Turn{}, fmt.Errorf("invalid custom API format or service address")
		}
		if err := ValidateCustomAuthentication(model.Authentication, model.APIKey); err != nil {
			return Turn{}, err
		}
		if err := ValidateCustomHeaders(model.Headers); err != nil {
			return Turn{}, err
		}
		model.BaseURL = CustomBaseURL(model.BaseURL, model.Provider)
	}
	adapter, ok := protocolAdapters[model.Provider]
	for _, message := range messages {
		if len(message.Images) > 0 && !adapter.imageInput {
			return Turn{}, fmt.Errorf("image input is not supported by %s", model.Provider)
		}
	}
	if !ok {
		return Turn{}, fmt.Errorf("unsupported model provider %q", model.Provider)
	}
	model, err := refreshRequestCredentials(ctx, model)
	if err != nil {
		return Turn{}, err
	}
	turn, err := adapter.turn(ctx, model, messages, schemas, onText, onReasoning)
	return turn, modelErrorForDisplay(err, model)
}
