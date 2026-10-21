package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"vista/internal/provider"
	"vista/internal/workerproto"
)

func observeModelRequest(command workerproto.Command, turnID int32, depth int, estimate int64, firstText *float64, turn provider.Turn, requestErr error, emit func(workerproto.Result) error) error {
	if depth != 0 {
		return nil
	}
	observation := workerproto.ModelObservation{
		RequestID: fmt.Sprintf("%s/%d", command.RunID, turnID), ModelID: command.Model.ID,
		ReasoningEffort: provider.ModelSelectionEffort(command.Model), Endpoint: provider.ModelEndpointID(command.Model),
		InputTokens: estimate, FirstTextMS: firstText, Outcome: "succeeded", Purpose: "conversation",
	}
	if turn.Usage != nil {
		if turn.Usage.InputTokens > 0 {
			observation.InputTokens = turn.Usage.InputTokens
		}
		observation.CacheReadTokens = turn.Usage.CacheReadTokens
	}
	if errors.Is(requestErr, context.Canceled) {
		observation.Outcome = "cancelled"
	} else if requestErr != nil {
		observation.Outcome = "failed"
	} else if firstText == nil {
		observation.Outcome = "no_text"
	}
	return emit(workerproto.Result{RunID: command.RunID, Kind: "model_observation", Turn: turnID, Observation: &observation})
}

func firstTextLatency(start time.Time) *float64 {
	ms := time.Since(start).Seconds() * 1000
	return &ms
}
