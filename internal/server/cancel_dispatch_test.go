package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"vista/internal/workerproto"
)

func TestAcceptedCancelPreventsPendingDispatch(t *testing.T) {
	app, sessionID := inputQueueFixture(t)
	sink := &contextNoticeAckSink{beforeWrite: func([]byte) error { return nil }}
	app.workers[sessionID] = &worker{input: sink}
	response := httptest.NewRecorder()
	app.session(response, httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/cancel", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("cancel: %d %s", response.Code, response.Body.String())
	}
	if err := app.dispatchWorker(app.ctx, sessionID, workerproto.Command{RunID: "run", Prompt: "work"}); err != nil {
		t.Fatal(err)
	}
	if sink.Len() != 0 {
		t.Fatalf("canceled run sent to Worker: %s", sink.String())
	}
	if _, err := app.store.RunningRunID(app.ctx, sessionID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("run still active: %v", err)
	}
	messages, err := app.store.MessagesBefore(app.ctx, sessionID, 0, 10)
	if err != nil || len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("saved input: %+v %v", messages, err)
	}
}

func TestDispatchedRunReceivesCancellationInOrder(t *testing.T) {
	app, sessionID := inputQueueFixture(t)
	sink := &contextNoticeAckSink{beforeWrite: func([]byte) error { return nil }}
	app.workers[sessionID] = &worker{input: sink}
	if err := app.dispatchWorker(app.ctx, sessionID, workerproto.Command{Action: "run", RunID: "run"}); err != nil {
		t.Fatal(err)
	}
	if err := app.cancelRun(app.ctx, sessionID, "run"); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(sink)
	for _, action := range []string{"run", "cancel"} {
		var cmd workerproto.Command
		if err := decoder.Decode(&cmd); err != nil || cmd.Action != action || cmd.RunID != "run" {
			t.Fatalf("command order: %+v %v", cmd, err)
		}
	}
}
