package agent

import (
	"vista/internal/provider"
	"vista/internal/workerproto"
)

func retainModelMessage(command workerproto.Command, turn int32, part string, index int, message provider.ChatMessage, depth int, emit func(workerproto.Result) error) error {
	if depth != 0 || command.SessionID == "" || command.MessageSequence == 0 {
		return nil
	}
	path, err := saveContextFile(command, []provider.ChatMessage{message}, "history-*.gob")
	if err != nil {
		return err
	}
	// Keep the file even if the ACK is lost: the commit may have succeeded.
	return emit(workerproto.Result{RunID: command.RunID, Kind: "model_history", Turn: turn, Status: part,
		History: &workerproto.ModelHistory{Path: path, Index: index, Tokens: message.EstimateTokens()}})
}

// A killed Worker can leave a durable call without a result. Restore a valid
// conversation with an explicit unknown outcome; loading never executes tools.
func completeInterruptedToolHistory(messages []provider.ChatMessage, pendingQuestionID string) []provider.ChatMessage {
	var complete []provider.ChatMessage
	for i := 0; i < len(messages); i++ {
		message := messages[i]
		complete = append(complete, message)
		if message.Role != "assistant" || len(message.ToolCalls) == 0 {
			continue
		}
		results := make(map[string]provider.ChatMessage, len(message.ToolCalls))
		for i+1 < len(messages) && messages[i+1].Role == "tool" {
			i++
			results[messages[i].ToolCallID] = messages[i]
		}
		for _, call := range message.ToolCalls {
			result, ok := results[call.ID]
			if !ok {
				if call.Function.Name == "question" && pendingQuestionID != "" && call.ID == pendingQuestionID && i == len(messages)-1 {
					continue // An unanswered question is not an interrupted tool.
				}
				result = provider.ChatMessage{Role: "tool", ToolCallID: call.ID, Content: `{"error":"Execution was interrupted before this tool result was recorded. The outcome and any side effects are unknown; verify the state before retrying."}`}
				if call.Function.Name == "question" {
					result.Content = `{"status":"unanswered","reason":"The question was interrupted or superseded by a new user message."}`
				}
			}
			complete = append(complete, result)
		}
	}
	return complete
}
