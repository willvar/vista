package agent

import (
	"context"
	"vista/internal/modeltrace"
	"vista/internal/workerproto"
)

func withModelDiagnostics(ctx context.Context, scope modeltrace.Scope, emit func(workerproto.Result) error) context.Context {
	return modeltrace.WithScope(ctx, scope, func(summary modeltrace.Summary) {
		// Diagnostics are optional. A notification failure must not change the
		// model result, and is repaired by the next index read or reconnect.
		_ = emit(workerproto.Result{RunID: summary.RunID, Kind: "model_diagnostic", DiagnosticID: summary.ID})
	})
}
