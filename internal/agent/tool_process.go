package agent

import (
	"context"
	"errors"
	"time"

	"vista/internal/instance"
	"vista/internal/provider"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

var shellSchema = map[string]any{"type": "function", "function": map[string]any{"name": "shell", "description": "Run a shell command in an explicit absolute directory. Returns up to 8192 bytes of output plus artifactId, truncated, and nextOffset. Continue truncated output with read_tool_output; do not rerun the command just to see more output. Timeout is at most 300 seconds.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"directory": map[string]any{"type": "string"}, "command": map[string]any{"type": "string"}, "timeoutSeconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 300}}, "required": []string{"directory", "command"}}}}

func (r *toolRuntime) shell(ctx context.Context, call provider.ToolCall) (string, error) {
	var input struct {
		Directory      string `json:"directory"`
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeoutSeconds"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = 60
	}
	stateDir, err := instance.Dir()
	if err != nil {
		return "", err
	}
	artifactEvent := func(result tools.ShellResult, status string, cause error) error {
		progress := workerproto.Result{RunID: r.parent.RunID, Kind: "artifact", Status: status, Artifact: &workerproto.Artifact{ID: result.ArtifactID, OutputPath: result.OutputPath, Bytes: result.Bytes, Complete: result.Complete}}
		if cause != nil {
			progress.Artifact.Error = cause.Error()
			if len(progress.Artifact.Error) > 512 {
				progress.Artifact.Error = progress.Artifact.Error[:512]
			}
		}
		return r.emit(progress)
	}
	result, err := tools.RunShellRecorded(ctx, stateDir, input.Directory, input.Command, time.Duration(input.TimeoutSeconds)*time.Second, func(result tools.ShellResult) error {
		return artifactEvent(result, "started", nil)
	}, func(result tools.ShellResult, err error) error {
		if err == nil {
			err = result.ExitError()
		}
		status := "succeeded"
		if err != nil || !result.Complete {
			status = "failed"
		}
		return artifactEvent(result, status, err)
	})
	if err == nil {
		err = result.ExitError()
	}
	if err != nil {
		output, encodeErr := marshalResult(result)
		return output, errors.Join(err, encodeErr)
	}
	return marshalResult(result)
}

var startBackgroundSchema = map[string]any{"type": "function", "function": map[string]any{"name": "start_background", "description": "Start a Session-owned shell job in an explicit absolute directory. It continues after this reply, up to 24 hours, and writes bounded output to a local file. Return its job ID to the user.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"directory": map[string]any{"type": "string"}, "command": map[string]any{"type": "string"}, "timeoutSeconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 86400}}, "required": []string{"directory", "command"}}}}

func (r *toolRuntime) startBackground(ctx context.Context, call provider.ToolCall) (string, error) {
	if r.jobs == nil {
		return "", errors.New("background jobs are unavailable")
	}
	var input struct {
		Directory      string `json:"directory"`
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeoutSeconds"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = 3600
	}
	stateDir, err := instance.Dir()
	if err != nil {
		return "", err
	}
	callID := call.ID
	if r.depth > 0 {
		callID = r.parent.TaskID + "/" + call.ID
	}
	job, err := r.jobs.Start(ctx, stateDir, r.parent.RunID, input.Directory, input.Command, time.Duration(input.TimeoutSeconds)*time.Second, commandSecrets(r.parent), callID, r.parent.TaskID, r.parent.ParentTaskID)
	if err != nil {
		return "", err
	}
	return marshalResult(map[string]any{"jobId": job.ID, "status": job.Status, "outputPath": job.OutputPath})
}

var stopBackgroundSchema = map[string]any{"type": "function", "function": map[string]any{"name": "stop_background", "description": "Stop a running Session-owned background shell job by its ID.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"jobId": map[string]any{"type": "string"}}, "required": []string{"jobId"}}}}

func (r *toolRuntime) stopBackground(ctx context.Context, call provider.ToolCall) (string, error) {
	if r.jobs == nil {
		return "", errors.New("background jobs are unavailable")
	}
	var input struct {
		JobID string `json:"jobId"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	if err := r.jobs.Stop(input.JobID); err != nil {
		return "", err
	}
	return marshalResult(map[string]any{"jobId": input.JobID, "status": "stopping"})
}
