package provider

import (
	"context"
	"slices"

	"vista/internal/replylanguage"
)

// prepareRequestMessages applies shared policy to a request-local copy. Keeping
// it out of canonical history prevents accumulation across calls and retries.
func prepareRequestMessages(ctx context.Context, messages []ChatMessage) []ChatMessage {
	// Older checkpoints used the system role for asynchronous results. Project
	// them as ordinary data without rewriting their persisted contents or order.
	for i, message := range messages {
		if message.RuntimeData && message.Role == "system" {
			messages = slices.Clone(messages)
			for j := i; j < len(messages); j++ {
				if messages[j].RuntimeData {
					messages[j].Role = "user"
				}
			}
			break
		}
	}
	if len(messages) > 0 && messages[0].Prefix != nil {
		return messages // The durable instruction series already owns language policy.
	}
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
