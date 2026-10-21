package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"vista/internal/instance"
	"vista/internal/store"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

var readToolOutputSchema = map[string]any{"type": "function", "function": map[string]any{"name": "read_tool_output", "description": "Read saved webfetch, shell, MCP, or other tool output in this Session by artifactId, or by callId from the current run. Continue at nextOffset without rerunning the original tool or downloading the URL again. Byte limit defaults to 8192 and can be up to 32768.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"artifactId": map[string]any{"type": "string"}, "callId": map[string]any{"type": "string"}, "offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 4, "maximum": tools.MaxOutputPageBytes}}}}}

func readToolOutput(ctx context.Context, arguments string, parent workerproto.Command) (string, error) {
	var input struct {
		ArtifactID string `json:"artifactId"`
		CallID     string `json:"callId"`
		Offset     int64  `json:"offset"`
		Limit      int    `json:"limit"`
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return "", err
	}
	if parent.SessionID == "" || (input.ArtifactID == "") == (input.CallID == "") || len(input.ArtifactID) > 64 || len(input.CallID) > 1024 || input.Offset < 0 {
		return "", errors.New("provide either artifactId or callId from this Session and a nonnegative offset")
	}
	limit, err := tools.OutputPageLimit(input.Limit)
	if err != nil {
		return "", err
	}
	stateDir, err := instance.Dir()
	if err != nil {
		return "", err
	}
	db, err := store.OpenReadOnly(filepath.Join(stateDir, "vista.db"))
	if err != nil {
		return "", err
	}
	defer db.Close()
	var artifact store.Artifact
	if input.ArtifactID != "" {
		artifact, err = db.GetArtifact(ctx, parent.SessionID, input.ArtifactID)
	} else {
		callID := input.CallID
		if parent.TaskID != "" && parent.TaskID != "main" && !strings.HasPrefix(callID, parent.TaskID+"/") {
			callID = parent.TaskID + "/" + callID
		}
		artifact, err = db.ToolArtifact(ctx, parent.SessionID, parent.RunID, callID)
	}
	if err != nil {
		return "", errors.New("saved tool output was not found in this Session; use an artifactId returned by the tool")
	}
	return fitOutputPage(limit, func(pageLimit int) (string, error) {
		page, err := tools.ReadOutputPage(stateDir, artifact.OutputPath, input.Offset, pageLimit)
		if err != nil {
			return "", err
		}
		return marshalResult(struct {
			tools.OutputPage
			ArtifactID string `json:"artifactId"`
			Complete   bool   `json:"complete"`
		}{OutputPage: page, ArtifactID: artifact.ID, Complete: artifact.Complete})
	})
}

// Byte limits apply before JSON escaping. Keep returned ranges inside the model
// budget too, so control characters or very short lines cannot hide nextOffset.
func fitOutputPage(limit int, read func(int) (string, error)) (string, error) {
	for {
		output, err := read(limit)
		if err != nil || !exceedsToolBudget(output) || limit <= 4 {
			return output, err
		}
		limit = max(4, limit/2)
	}
}
