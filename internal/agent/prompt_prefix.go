package agent

import (
	"context"
	"strings"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/replylanguage"
	"vista/internal/workerproto"
)

const instructionSnapshotPrefix = "Current instruction snapshot. This replaces earlier instruction snapshots, not the user's current request. Directory-scoped rules and explicit current requests retain their precedence.\n\n"
const languageControlPrefix = "Current reply-language default; explicit current user language requests retain precedence:\n"

func promptPrefixProfile(model modelcfg.Model) string {
	return compactionModelKey(model) + "\x00" + model.ConnectionID + "\x00" + string(model.ResponsesRoute)
}

func prefixText(current, language string) string {
	if language == "" {
		return current
	}
	return current + "\n\n" + language
}

func languageControl(language string) provider.ChatMessage {
	if language == "" {
		language = "Use the same language as the user's message; follow explicit language requests."
	}
	return provider.ChatMessage{Role: "system", Content: languageControlPrefix + language, LanguageControl: true}
}

func isLanguageControl(message provider.ChatMessage) bool {
	// Old checkpoints predate the local flag. Match only Vista's system control,
	// excluding full snapshots, runtime reports and the initial prefix.
	return message.LanguageControl || message.Role == "system" && !message.InstructionUpdate && !message.RuntimeData && message.Prefix == nil && strings.HasPrefix(message.Content, languageControlPrefix)
}

// Metadata-only entries use the existing acknowledged history ledger. They are
// removed before token estimation and never enter a provider request.
func pinPromptPrefix(messages []provider.ChatMessage, current, profile string, languages ...string) ([]provider.ChatMessage, []provider.ChatMessage) {
	previous := messages[0].Prefix
	retained := make([]provider.ChatMessage, 1, len(messages)+1)
	retained[0] = messages[0]
	for _, message := range messages[1:] {
		if message.Role == "" && message.Prefix != nil {
			previous = message.Prefix
			continue
		}
		retained = append(retained, message)
	}
	language := ""
	if previous != nil {
		language = previous.Language
	}
	if len(languages) > 0 {
		language = languages[0]
	}
	state := &provider.PromptPrefix{Profile: profile, Base: prefixText(current, language), Current: current, Language: language}
	if previous != nil && previous.Profile == profile && previous.Base != "" {
		state.Base = previous.Base
	}
	retained[0].Content, retained[0].Prefix = state.Base, state
	if previous != nil && previous.Profile == profile && previous.Current == current && previous.Language == language {
		return retained, nil
	}
	// Idle maintenance cannot split an unanswered native question from its answer.
	last := retained[len(retained)-1]
	if previous != nil && last.Role == "assistant" && len(last.ToolCalls) > 0 {
		retained[0].Content, retained[0].Prefix = previous.Base, previous
		return retained, nil
	}
	pending := []provider.ChatMessage{{Prefix: state}}
	if previous != nil {
		update := languageControl(language)
		if previous.Profile != profile || previous.Current != current {
			update = provider.ChatMessage{Role: "system", Content: instructionSnapshotPrefix + prefixText(current, language), InstructionUpdate: true}
		}
		retained = append(retained, update)
		pending = append(pending, update)
	}
	return retained, pending
}

func retainPromptPrefix(command workerproto.Command, turn int32, messages []provider.ChatMessage, emit func(workerproto.Result) error) error {
	if len(messages) == 0 || command.SessionID == "" || command.MessageSequence == 0 {
		return nil
	}
	path, err := saveContextFile(command, messages, "prefix-*.gob")
	if err != nil {
		return err
	}
	var tokens int64
	for _, message := range messages {
		if message.Role != "" {
			tokens += message.EstimateTokens()
		}
	}
	// Several committed inputs can update the prefix before the same model turn.
	// Their input boundaries keep those updates distinct and ACK retries stable.
	return emit(workerproto.Result{RunID: command.RunID, Kind: "model_history", Turn: max(1, turn), Status: "prefix", History: &workerproto.ModelHistory{Path: path, Index: int(command.MessageSequence), Tokens: tokens}})
}

func (prepared *preparedContext) updateLanguage(ctx context.Context, command workerproto.Command, messages []provider.ChatMessage, turn int32, emit func(workerproto.Result) error) ([]provider.ChatMessage, error) {
	previous := messages[0].Prefix
	if previous == nil {
		return messages, nil
	}
	language, profile := replylanguage.Instruction(ctx), promptPrefixProfile(command.Model)
	if previous.Language == language && previous.Profile == profile {
		return messages, nil
	}
	state := *previous
	state.Language, state.Profile = language, profile
	next := append([]provider.ChatMessage(nil), messages...)
	pending := []provider.ChatMessage{{Prefix: &state}}
	if previous.Language != language {
		// A locale change must not restate stale global rules, memories or run facts.
		update := languageControl(language)
		next = append(next, update)
		pending = append(pending, update)
	}
	if err := retainPromptPrefix(command, turn, pending, emit); err != nil {
		return messages, err
	}
	next[0].Prefix = &state
	return next, nil
}
