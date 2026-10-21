package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"vista/internal/resources"
)

type ChatMessage struct {
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	Resources []resources.Ref `json:"-"`
	Images    []Image         `json:"-"`
	// A one-request activation, deliberately unexported so saved history and
	// checkpoints retain references without reactivating historical images.
	visualInput bool
	ToolCallID  string     `json:"tool_call_id,omitempty"`
	ToolCalls   []ToolCall `json:"tool_calls,omitempty"`
	// Runtime results remain compressible history even when their wire role is
	// system. This local marker is retained in checkpoints, never sent on the API.
	RuntimeData bool `json:"-"`
	// Signed content blocks are needed when an Anthropic thinking turn calls tools.
	AnthropicBlocks   []json.RawMessage `json:"-"`
	BedrockParts      []BedrockPart     `json:"-"`
	MistralParts      []MistralPart     `json:"-"`
	ReasoningDetails  []json.RawMessage `json:"-"`
	ReasoningText     string            `json:"-"`
	ReasoningField    string            `json:"-"`
	ResponseReasoning []json.RawMessage `json:"-"`
	ResponseOutput    []json.RawMessage `json:"-"`
}

func (message ChatMessage) WithVisualInput(enabled bool) ChatMessage {
	message.visualInput = enabled && len(message.Resources) > 0
	return message
}

func (message ChatMessage) VisualInput() bool { return message.visualInput }

// ResourceText describes retained originals without implying that their pixels
// are present. Request assembly and token estimates use the same metadata.
func (message ChatMessage) ResourceText() string {
	var text strings.Builder
	for _, ref := range message.Resources {
		fmt.Fprintf(&text, "\n[Resource %s: %s; %s; %d bytes]", ref.Hash, ref.Name, ref.MediaType, ref.Bytes)
	}
	if len(message.Resources) > 0 && !message.visualInput && len(message.Images) == 0 {
		text.WriteString("\n[Visual content is not attached; use read_resource with the hash to view it.]")
	}
	return text.String()
}

type MistralPart struct {
	Type string
	Text string
}
type BedrockPart struct {
	Kind      string
	Text      string
	Signature string
	Redacted  []byte
	Call      ToolCall
}
type ToolCall struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	ThoughtSignature string `json:"-"`
	GeminiID         string `json:"-"`
	Function         struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
type Turn struct {
	Text              string
	Calls             []ToolCall
	HostedCalls       []HostedCall
	Sources           []string
	FinishReason      string
	Usage             *ModelUsage
	AnthropicBlocks   []json.RawMessage
	BedrockParts      []BedrockPart
	MistralParts      []MistralPart
	ReasoningDetails  []json.RawMessage
	ReasoningText     string
	ReasoningField    string
	ResponseReasoning []json.RawMessage
	ResponseOutput    []json.RawMessage
}

type HostedCall struct {
	ID      string
	Name    string
	Result  string
	Failure bool
}

type ModelUsage struct {
	InputTokens     int64
	OutputTokens    int64
	CacheReadTokens int64
	ReasoningTokens int64
}

// AssistantMessage preserves the provider-owned state required by the next model
// request. content may be an Agent-selected preview; signed blocks and native
// response items remain unchanged. Callers treat those fields as opaque.
func (turn Turn) AssistantMessage(content string) ChatMessage {
	return ChatMessage{
		Role: "assistant", Content: content, ToolCalls: turn.Calls,
		AnthropicBlocks: turn.AnthropicBlocks, BedrockParts: turn.BedrockParts,
		MistralParts: turn.MistralParts, ReasoningDetails: turn.ReasoningDetails,
		ReasoningText: turn.ReasoningText, ReasoningField: turn.ReasoningField,
		ResponseReasoning: turn.ResponseReasoning, ResponseOutput: turn.ResponseOutput,
	}
}

// HostedContinuation returns the provider history needed to continue a turn that
// contains provider-hosted work but no local calls or final answer. The Agent
// decides when to continue; the provider owns which replay fields are required.
func (turn Turn) HostedContinuation() (ChatMessage, bool) {
	if len(turn.HostedCalls) == 0 || len(turn.ResponseOutput) == 0 {
		return ChatMessage{}, false
	}
	return ChatMessage{Role: "assistant", ResponseOutput: turn.ResponseOutput, ResponseReasoning: turn.ResponseReasoning}, true
}

// SummaryText projects a message for the execution summarizer. Signed thinking
// and other opaque continuation fields remain on retained messages, rather than
// being presented as instructions to the summary model. Hosted-only output keeps
// its provider output fallback because it can carry the only result evidence.
func (message ChatMessage) SummaryText() string {
	var text strings.Builder
	role := message.Role
	if message.RuntimeData {
		role = "runtime result"
	}
	fmt.Fprintf(&text, "[%s %s]\n%s\n", role, message.ToolCallID, message.Content)
	for _, ref := range message.Resources {
		fmt.Fprintf(&text, "[resource %s %s %s %d bytes; use read_resource to view]\n", ref.Hash, ref.Name, ref.MediaType, ref.Bytes)
	}
	for _, call := range message.ToolCalls {
		fmt.Fprintf(&text, "[tool call %s %s] %s\n", call.ID, call.Function.Name, call.Function.Arguments)
	}
	if message.Content == "" && len(message.ToolCalls) == 0 {
		for _, raw := range message.ResponseOutput {
			fmt.Fprintf(&text, "[provider output] %s\n", raw)
		}
	}
	return text.String()
}

// EstimateTextTokens is a conservative text heuristic, not a model tokenizer.
func EstimateTextTokens(text string) int64 {
	var ascii, other int64
	for _, r := range text {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			other++
		}
	}
	return (ascii+2)/3 + other*2
}

// EstimateTokens includes opaque continuation state as well as visible content.
func (message ChatMessage) EstimateTokens() int64 {
	total := int64(8) + EstimateTextTokens(message.Content)
	for _, call := range message.ToolCalls {
		total += 12 + EstimateTextTokens(call.ID+call.Function.Name+call.Function.Arguments+call.ThoughtSignature+call.GeminiID)
	}
	for _, raw := range [][]json.RawMessage{message.AnthropicBlocks, message.ReasoningDetails, message.ResponseReasoning, message.ResponseOutput} {
		for _, item := range raw {
			total += EstimateTextTokens(string(item))
		}
	}
	for _, part := range message.BedrockParts {
		total += EstimateTextTokens(part.Text+part.Signature+part.Call.Function.Arguments) + int64(len(part.Redacted))/3
	}
	for _, part := range message.MistralParts {
		total += EstimateTextTokens(part.Text)
	}
	total += EstimateTextTokens(message.ReasoningText)
	// Encoded image bytes do not map to visual tokens. Include a small
	// provisional allowance until a real reported input count is available.
	total += EstimateTextTokens(message.ResourceText())
	images := len(message.Images)
	if message.visualInput {
		images = max(images, len(message.Resources))
	}
	total += int64(images) * 1024
	return total
}

// EstimateRequestTokens measures messages and tool schemas with the same heuristic.
func EstimateRequestTokens(messages []ChatMessage, schemas []map[string]any) int64 {
	var total int64
	for _, message := range messages {
		total += message.EstimateTokens()
	}
	encoded, _ := json.Marshal(schemas)
	return total + EstimateTextTokens(string(encoded))
}
