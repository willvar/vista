package worker

import (
	"encoding/json"
	"fmt"
	"io"

	"vista/internal/workerproto"
)

// Encode completely before touching the pipe. Individually bounded fields can
// still exceed the scanner budget when combined or escaped. Return an ordinary
// execution error rather than writing a partial/oversized frame to the Server.
func writeResult(output io.Writer, result workerproto.Result) error {
	frame, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if len(frame)+1 >= workerproto.MaxResultFrame {
		return fmt.Errorf("Worker %s result exceeds the %d-byte transport frame limit", result.Kind, workerproto.MaxResultFrame)
	}
	frame = append(frame, '\n')
	n, err := output.Write(frame)
	if err == nil && n != len(frame) {
		err = io.ErrShortWrite
	}
	return err
}
