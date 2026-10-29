package server

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"vista/internal/workerproto"
)

// rpc transports only control metadata and references, never image bytes.
func (w *worker) rpc(ctx context.Context, command workerproto.Command) (workerproto.Result, error) {
	id := uuid.NewString()
	command.RPCID = id
	if command.RunID == "" {
		command.RunID = id
	}
	response := make(chan workerproto.Result, 1)
	w.rpcMu.Lock()
	if w.pendingRPC == nil {
		w.pendingRPC = map[string]chan workerproto.Result{}
	}
	w.pendingRPC[id] = response
	w.rpcMu.Unlock()
	defer func() { w.rpcMu.Lock(); delete(w.pendingRPC, id); w.rpcMu.Unlock() }()
	if err := w.send(command); err != nil {
		return workerproto.Result{}, err
	}
	select {
	case result := <-response:
		if result.Status != "succeeded" {
			return result, errors.New(result.Content)
		}
		return result, nil
	case <-w.done:
		return workerproto.Result{}, errors.New("conversation Worker stopped; resource operation was interrupted")
	case <-ctx.Done():
		_ = w.send(workerproto.Command{Action: "cancel_rpc", RunID: id, RPCID: id})
		// Give the Worker a chance to close its staged input before caller cleanup.
		select {
		case <-response:
		case <-w.done:
		case <-time.After(time.Second):
		}
		return workerproto.Result{}, ctx.Err()
	}
}
func (w *worker) receiveRPC(result workerproto.Result) {
	w.rpcMu.Lock()
	pending := w.pendingRPC[result.RPCID]
	w.rpcMu.Unlock()
	if pending != nil {
		select {
		case pending <- result:
		default:
		}
	}
}
