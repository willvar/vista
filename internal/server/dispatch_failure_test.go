package server

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	modelcfg "vista/internal/model"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestDispatchNetworkFailureFinishesRunAndAllowsNewWork(t *testing.T) {
	for _, contents := range []string{"{", `{"version":2,"network":{"globalProxy":"missing","profiles":[]}}`} {
		t.Run(contents, func(t *testing.T) {
			app, session := inputQueueFixture(t)
			app.configDir = t.TempDir()
			if err := os.Chmod(app.configDir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(app.configDir, "config.json")
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			messages, err := app.store.MessagesBefore(app.ctx, session, 0, 10)
			if err != nil || len(messages) != 1 {
				t.Fatalf("messages: %+v %v", messages, err)
			}
			sink := &imageCommandSink{}
			app.workers[session] = &worker{input: sink}
			before := app.updates.snapshot().Revisions["sessions"]
			if err := app.dispatchRun(app.ctx, session, "run", messages[0], modelcfg.Model{ID: "model"}); err == nil {
				t.Fatal("invalid network configuration accepted")
			}
			if sink.Len() != 0 {
				t.Fatal("unprepared run reached Worker")
			}
			if _, err := app.store.RunningRunID(app.ctx, session); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("failed dispatch left run active: %v", err)
			}
			if app.updates.snapshot().Revisions["sessions"] != before+1 {
				t.Fatal("committed interruption was not published")
			}
			if err := os.WriteFile(path, []byte(`{"version":2}`), 0600); err != nil {
				t.Fatal(err)
			}
			message, _, err := app.store.BeginRun(app.ctx, session, "next", "next-input", "new work")
			if err != nil {
				t.Fatalf("repaired configuration still blocked: %v", err)
			}
			if err := app.dispatchRun(app.ctx, session, "next", message, modelcfg.Model{ID: "model"}); err != nil {
				t.Fatal(err)
			}
			var command workerproto.Command
			if err := json.NewDecoder(sink).Decode(&command); err != nil || command.RunID != "next" {
				t.Fatalf("follow-up dispatch: %+v %v", command, err)
			}
		})
	}
}
