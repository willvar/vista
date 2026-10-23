package provider

import (
	"context"
	"slices"

	"vista/internal/replylanguage"
)

// prepareRequestMessages applies shared policy to a request-local copy. Keeping
// it out of canonical history prevents accumulation across calls and retries.
func prepareRequestMessages(ctx context.Context, messages []ChatMessage) []ChatMessage {
	instruction := replylanguage.Instruction(ctx)
	if instruction == "" {
		return messages
	}
	if len(messages) > 0 && messages[0].Role == "system" && !messages[0].RuntimeData {
		prepared := slices.Clone(messages)
		prepared[0].Content += "\n\n" + instruction
		return prepared
	}
	prepared := make([]ChatMessage, 0, len(messages)+1)
	prepared = append(prepared, ChatMessage{Role: "system", Content: instruction})
	return append(prepared, messages...)
}
