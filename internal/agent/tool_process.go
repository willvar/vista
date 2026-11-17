package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"vista/internal/instance"
	"vista/internal/provider"
	"vista/internal/toolinput"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

func shellCommandParameter() map[string]any {
	return map[string]any{"type": "string", "description": fmt.Sprintf("Nonempty script for the platform shell (POSIX sh or Windows PowerShell). Vista executes it from a private temporary file, so multiline scripts do not need a separate write_file call. The complete JSON arguments, including directory and escaping, must fit within %d UTF-8 bytes. For larger source, execute an existing script file by path.", toolinput.MaxArgumentsBytes)}
}

var shellSchema = map[string]any{"type": "function", "function": map[string]any{"name": "shell", "description": "Run a foreground shell command in an explicit absolute directory and wait for it to finish. Timeout defaults to 60 seconds and is at most 300 seconds. Timeout or cancellation terminates the command and its managed descendants; remaining descendants are also cleaned up when the command ends. Use start_background for GUI applications, servers, watchers, or other programs that must keep running. Returns short output in full; long output shares 8192 bytes between preview (head) and tail (end, with original byte offsets). artifactId retains the complete output; nextOffset starts the omitted middle, ending at tail.offset. Read missing evidence with read_tool_output rather than rerunning the command. For tests, set their own diagnostic timeout below timeoutSeconds so failures include a test stack trace before the shell terminates.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"directory": map[string]any{"type": "string"}, "command": shellCommandParameter(), "timeoutSeconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 300, "description": "Seconds to wait before terminating the command and its managed descendants; defaults to 60."}}, "required": []string{"directory", "command"}}}}

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
			progress.Artifact.Error = workerproto.TextPreview(cause.Error(), workerproto.MaxErrorBytes)
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

var startBackgroundSchema = map[string]any{"type": "function", "function": map[string]any{"name": "start_background", "description": "Start a Session-owned shell job in an explicit absolute directory. Use for GUI applications, servers, watchers, or other programs that must keep running after this reply or run cancellation. No timeout by default: the job runs until it exits, is stopped, or its Session Worker exits. Closing the conversation or stopping/restarting Vista terminates it. Writes bounded output to a local file. Returns jobId, status, and outputPath, plus expiresAt and optional warnAt when a timeout is set. Optional warnBeforeSeconds sends one advance notice; reading or receiving it never extends the hard deadline. Inspect progress and explicitly update_background_timeout or stop_background when needed. Never extend a user-imposed hard deadline without authorization. Return its job ID to the user.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"directory": map[string]any{"type": "string"}, "command": shellCommandParameter(), "timeoutSeconds": map[string]any{"type": "integer", "minimum": 1, "description": "Optional positive number of seconds before terminating the command and its managed descendants; omit for no deadline."}, "warnBeforeSeconds": map[string]any{"type": "integer", "minimum": 1, "description": "Optional advance warning in seconds; requires timeoutSeconds and must be smaller. Omit for no warning. There is no automatic grace period."}}, "required": []string{"directory", "command"}}}}

func (r *toolRuntime) startBackground(ctx context.Context, call provider.ToolCall) (string, error) {
	if r.jobs == nil {
		return "", errors.New("background jobs are unavailable")
	}
	var input struct {
		Directory         string `json:"directory"`
		Command           string `json:"command"`
		TimeoutSeconds    *int64 `json:"timeoutSeconds"`
		WarnBeforeSeconds *int64 `json:"warnBeforeSeconds"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	timeout, warnBefore, err := backgroundTiming(input.TimeoutSeconds, input.WarnBeforeSeconds)
	if err != nil {
		return "", err
	}
	stateDir, err := instance.Dir()
	if err != nil {
		return "", err
	}
	callID := call.ID
	if r.depth > 0 {
		callID = r.parent.TaskID + "/" + call.ID
	}
	job, err := r.jobs.Start(ctx, stateDir, r.parent.RunID, input.Directory, input.Command, timeout, warnBefore, commandSecrets(r.parent), callID, r.parent.TaskID, r.parent.ParentTaskID)
	if err != nil {
		return "", err
	}
	output := map[string]any{"jobId": job.ID, "status": job.Status, "outputPath": job.OutputPath}
	if job.ExpiresAt != "" {
		output["expiresAt"] = job.ExpiresAt
	}
	if job.WarnAt != "" {
		output["warnAt"] = job.WarnAt
	}
	return marshalResult(output)
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

// The arguments are integer seconds; do not clamp invalid timing combinations.
func backgroundTiming(timeoutSeconds, warnBeforeSeconds *int64) (time.Duration, time.Duration, error) {
	var timeout, warning time.Duration
	for _, field := range []struct {
		name   string
		value  *int64
		target *time.Duration
	}{
		{"timeoutSeconds", timeoutSeconds, &timeout}, {"warnBeforeSeconds", warnBeforeSeconds, &warning},
	} {
		if field.value == nil {
			continue
		}
		if *field.value <= 0 || *field.value > int64(time.Duration(1<<63-1)/time.Second) {
			return 0, 0, fmt.Errorf("%s must be positive and fit within a supported duration", field.name)
		}
		*field.target = time.Duration(*field.value) * time.Second
	}
	if warning > 0 && (timeout == 0 || warning >= timeout) {
		return 0, 0, errors.New("warnBeforeSeconds requires timeoutSeconds and must be smaller")
	}
	return timeout, warning, nil
}

var updateBackgroundTimeoutSchema = map[string]any{"type": "function", "function": map[string]any{
	"name":        "update_background_timeout",
	"description": "Adjust a running Session-owned job's hard deadline. timeoutSeconds is the new remaining time, measured from this update request, not the original start. warnBeforeSeconds is optional and replaces the previous warning setting; omit for no warning. Supply the exact last observed expiresAt as expectedExpiresAt, or an empty string for a previously unlimited job; inspect current job metadata with read_tool_output(jobId) after a conflict. Only a successful update replaces the old deadline. Never extend an explicit user hard deadline without authorization.",
	"parameters": map[string]any{"type": "object", "properties": map[string]any{
		"jobId": map[string]any{"type": "string"}, "expectedExpiresAt": map[string]any{"type": "string"},
		"timeoutSeconds":    map[string]any{"type": "integer", "minimum": 1},
		"warnBeforeSeconds": map[string]any{"type": "integer", "minimum": 1},
	}, "required": []string{"jobId", "expectedExpiresAt", "timeoutSeconds"}},
}}

func (r *toolRuntime) updateBackgroundTimeout(ctx context.Context, call provider.ToolCall) (string, error) {
	if r.jobs == nil {
		return "", errors.New("background jobs are unavailable")
	}
	var input struct {
		JobID             string  `json:"jobId"`
		ExpectedExpiresAt *string `json:"expectedExpiresAt"`
		TimeoutSeconds    *int64  `json:"timeoutSeconds"`
		WarnBeforeSeconds *int64  `json:"warnBeforeSeconds"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	if input.ExpectedExpiresAt == nil || input.TimeoutSeconds == nil {
		return "", errors.New("expectedExpiresAt and timeoutSeconds are required")
	}
	timeout, warning, err := backgroundTiming(input.TimeoutSeconds, input.WarnBeforeSeconds)
	if err != nil {
		return "", err
	}
	job, err := r.jobs.UpdateTimeout(ctx, input.JobID, *input.ExpectedExpiresAt, timeout, warning)
	if err != nil {
		return "", err
	}
	return marshalResult(map[string]any{"jobId": job.ID, "status": job.Status, "expiresAt": job.ExpiresAt, "warnAt": job.WarnAt})
}
