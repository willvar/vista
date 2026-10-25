package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	modelcfg "vista/internal/model"
)

func isBedrockMantle(model modelcfg.Model) bool {
	return model.Provider == "bedrock-mantle-chat" || model.Provider == "bedrock-mantle-responses"
}

func bedrockMantleTurn(ctx context.Context, model modelcfg.Model, messages []ChatMessage, schemas []map[string]any, onText, onReasoning func(string) error) (Turn, error) {
	if err := validateImageMessages(messages); err != nil {
		return Turn{}, err
	}
	for _, message := range messages {
		if len(message.Images) > 0 && model.Provider != "bedrock-mantle-responses" {
			return Turn{}, errors.New("image input is not supported by Bedrock Mantle Chat")
		}
	}
	if model.AWSRegion == "" || model.BaseURL == "" || model.APIKey == "" && (model.AWSAccessID == "" || model.AWSSecret == "") {
		return Turn{}, errors.New("Bedrock Mantle region, endpoint, and credentials are required")
	}
	if model.Provider == "bedrock-mantle-chat" {
		return openAIChatTurnWithReasoning(ctx, model, messages, schemas, onText, onReasoning)
	}
	return openAIResponsesTurnWithReasoning(ctx, model, messages, schemas, onText, onReasoning)
}

func doModelRequest(request *http.Request, model modelcfg.Model) (*http.Response, error) {
	if model.Custom {
		ApplyCustomRequestHeaders(request.Header, model.Authentication, model.APIKey, model.Headers)
	} else {
		for name, value := range model.Headers {
			request.Header.Set(name, value)
		}
	}
	if model.ProviderID == "opencode-go" && model.SessionID != "" {
		request.Header.Set("x-opencode-session", model.SessionID)
	}
	if !isBedrockMantle(model) || model.APIKey != "" {
		return doStreamingRequest(request, client, modelReadIdleTimeout)
	}
	custom := &http.Client{
		Transport: mantleTransport{base: tracedModelHTTPTransport, model: model},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("Bedrock Mantle endpoint redirected")
		},
	}
	return doStreamingRequest(request, custom, modelReadIdleTimeout)
}

type mantleTransport struct {
	base  http.RoundTripper
	model modelcfg.Model
}

func (t mantleTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	hash := sha256.New()
	if request.Body != nil && request.Body != http.NoBody {
		if request.GetBody == nil {
			return nil, errors.New("Bedrock Mantle signing requires a replayable request body")
		}
		body, err := request.GetBody()
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(hash, body)
		closeErr := body.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	signed := request.Clone(request.Context())
	credentials := aws.Credentials{AccessKeyID: t.model.AWSAccessID, SecretAccessKey: t.model.AWSSecret, SessionToken: t.model.AWSSession}
	if err := v4.NewSigner().SignHTTP(request.Context(), credentials, signed, hex.EncodeToString(hash.Sum(nil)), "bedrock-mantle", t.model.AWSRegion, time.Now()); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(signed)
}
