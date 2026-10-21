package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"vista/internal/instance"
	"vista/internal/workerproto"
)

// recordToolOutput preserves full output using the existing Session artifact.
func recordToolOutput(ctx context.Context, runID, callID string, output []byte, emit func(workerproto.Result) error) (*workerproto.OutputRef, error) {
	return recordToolOutputStream(ctx, runID, callID, func(w io.Writer) error {
		count, err := w.Write(output)
		if err == nil && count != len(output) {
			err = io.ErrShortWrite
		}
		return err
	}, emit)
}

// recordToolOutputStream saves output without buffering the complete result in
// memory. A failed producer leaves an explicitly incomplete, readable artifact.
func recordToolOutputStream(ctx context.Context, runID, callID string, write func(io.Writer) error, emit func(workerproto.Result) error) (*workerproto.OutputRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stateDir, err := instance.Dir()
	if err != nil {
		return nil, err
	}
	outputDir := filepath.Join(stateDir, "outputs")
	if err := os.MkdirAll(outputDir, 0700); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(outputDir, "tool-*.txt")
	if err != nil {
		return nil, err
	}
	path := file.Name()
	id := uuid.NewString()
	artifact := &workerproto.Artifact{ID: id, OutputPath: path, CallID: callID}
	if err := emit(workerproto.Result{RunID: runID, Kind: "artifact", Status: "started", Artifact: artifact}); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}
	writeErr := write(file)
	if info, err := file.Stat(); err == nil {
		artifact.Bytes = info.Size()
	} else if writeErr == nil {
		writeErr = err
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	artifact.Complete = writeErr == nil
	status := "succeeded"
	if writeErr != nil {
		status = "failed"
		artifact.Error = writeErr.Error()
		if len(artifact.Error) > 512 {
			artifact.Error = artifact.Error[:512]
		}
	}
	if err := emit(workerproto.Result{RunID: runID, Kind: "artifact", Status: status, Artifact: artifact}); err != nil {
		return nil, fmt.Errorf("save tool output: %v; record artifact: %w", writeErr, err)
	}
	reference := &workerproto.OutputRef{ArtifactID: id, OutputPath: path, Bytes: artifact.Bytes}
	if writeErr != nil {
		return reference, fmt.Errorf("tool output incomplete; partial output at %s (%d bytes): %w", path, artifact.Bytes, writeErr)
	}
	return reference, nil
}
