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

var readToolOutputSchema = map[string]any{"type": "function", "function": map[string]any{"name": "read_tool_output", "description": "Read saved webfetch, shell, MCP, or other tool output in this Session by artifactId or jobId, or by callId from the current run. Continue at nextOffset without rerunning the original tool or downloading the URL again. Byte limit defaults to 8192 and can be up to 32768. Optional pattern searches saved text (Go regex, or literal=true) and returns matching lines, contextLines and byte positions. Continue at nextOffset without rerunning the tool.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"artifactId": map[string]any{"type": "string"}, "callId": map[string]any{"type": "string"}, "jobId": map[string]any{"type": "string"}, "offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 4, "maximum": tools.MaxOutputPageBytes}, "pattern": map[string]any{"type": "string"}, "literal": map[string]any{"type": "boolean"}, "ignoreCase": map[string]any{"type": "boolean"}, "contextLines": map[string]any{"type": "integer", "minimum": 0, "maximum": tools.MaxToolResultLines}}}}}

func readToolOutput(ctx context.Context, arguments string, parent workerproto.Command) (string, error) {
	var input struct {
		ArtifactID   string  `json:"artifactId"`
		JobID        string  `json:"jobId"`
		CallID       string  `json:"callId"`
		Offset       int64   `json:"offset"`
		Limit        int     `json:"limit"`
		Pattern      *string `json:"pattern"`
		Literal      bool    `json:"literal"`
		IgnoreCase   bool    `json:"ignoreCase"`
		ContextLines int     `json:"contextLines"`
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return "", err
	}
	targets := 0
	for _, id := range []string{input.ArtifactID, input.CallID, input.JobID} {
		if id != "" {
			targets++
		}
	}
	if parent.SessionID == "" || targets != 1 || len(input.ArtifactID) > 64 || len(input.JobID) > 64 || len(input.CallID) > 2048 || input.Offset < 0 {
		return "", errors.New("provide one artifactId, jobId, or callId from this Session and a nonnegative offset")
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
	if input.JobID != "" {
		job, jobErr := db.GetJob(ctx, parent.SessionID, input.JobID)
		err = jobErr
		artifact = store.Artifact{ID: job.ID, OutputPath: job.OutputPath, Complete: job.Complete}
	} else if input.ArtifactID != "" {
		artifact, err = db.GetArtifact(ctx, parent.SessionID, input.ArtifactID)
	} else {
		callID := input.CallID
		if parent.TaskID != "" && parent.TaskID != "main" && !strings.HasPrefix(callID, parent.TaskID+"/") {
			callID = parent.TaskID + "/" + callID
		}
		artifact, err = db.ToolArtifact(ctx, parent.SessionID, parent.RunID, callID)
	}
	if err != nil {
		return "", errors.New("saved tool output was not found in this Session; use an artifactId or jobId returned by the tool")
	}
	artifactID, jobID := artifact.ID, ""
	if input.JobID != "" {
		artifactID, jobID = "", artifact.ID
	}
	return fitOutputPage(limit, func(pageLimit int) (string, error) {
		if input.Pattern != nil {
			page, err := tools.SearchOutput(ctx, stateDir, artifact.OutputPath, input.Offset, pageLimit, *input.Pattern, input.Literal, input.IgnoreCase, input.ContextLines)
			if err != nil {
				return "", err
			}
			return marshalResult(struct {
				tools.OutputSearchPage
				ArtifactID string `json:"artifactId,omitempty"`
				JobID      string `json:"jobId,omitempty"`
				Complete   bool   `json:"complete"`
			}{OutputSearchPage: page, ArtifactID: artifactID, JobID: jobID, Complete: artifact.Complete})
		}
		page, err := tools.ReadOutputPage(stateDir, artifact.OutputPath, input.Offset, pageLimit)
		if err != nil {
			return "", err
		}
		return marshalResult(struct {
			tools.OutputPage
			ArtifactID string `json:"artifactId,omitempty"`
			JobID      string `json:"jobId,omitempty"`
			Complete   bool   `json:"complete"`
		}{OutputPage: page, ArtifactID: artifactID, JobID: jobID, Complete: artifact.Complete})
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
