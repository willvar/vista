package agent

import (
	"context"

	"vista/internal/agentconfig"
	"vista/internal/appdirs"
	"vista/internal/provider"
	"vista/internal/replylanguage"
	"vista/internal/workerproto"
)

func prepareSessionContext(ctx context.Context, command workerproto.Command) (preparedContext, error) {
	dir, err := appdirs.Config()
	if err != nil {
		return preparedContext{}, err
	}
	settings, err := agentconfig.ReadSettings(dir)
	if err != nil {
		return preparedContext{}, err
	}
	command.Action = "compact" // Load existing context without appending an input.
	return prepareContext(ctx, command, nil, 0, settings.DelegationDepth)
}

// EstimateSessionContext uses the same assembly path as a new main request.
// Retained tool turns, checkpoints, newer full messages, instructions and tool
// schemas are all included, including while the Session is idle.
// This is read-only and never calls a model or starts a Worker.
func EstimateSessionContext(ctx context.Context, command workerproto.Command) (int64, error) {
	ctx = replylanguage.WithDefault(ctx, command.ReplyLanguage)
	prepared, err := prepareSessionContext(ctx, command)
	if err != nil {
		return 0, err
	}
	return provider.EstimateRequestTokens(ctx, prepared.messages, prepared.applyModel(prepared.messages, command.Model)), nil
}

func emitContextUsage(ctx context.Context, command workerproto.Command, messages []provider.ChatMessage, schemas []map[string]any, depth int, emit func(workerproto.Result) error) error {
	if depth != 0 {
		return nil
	}
	return emit(workerproto.Result{RunID: command.RunID, Kind: "context_usage", ContextUsage: &workerproto.ContextUsage{
		RunID: command.RunID, Tokens: provider.EstimateRequestTokens(ctx, messages, schemas),
	}})
}
