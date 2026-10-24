package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"vista/internal/store"
	"vista/internal/workerproto"
)

func inputQueueFixture(t *testing.T) (*App, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const id = "00000000-0000-4000-8000-000000000055"
	if _, err := db.Create(context.Background(), id, "queue"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.BeginRun(context.Background(), id, "run", "first", "work"); err != nil {
		t.Fatal(err)
	}
	return &App{ctx: context.Background(), store: db, dir: t.TempDir(), workers: map[string]*worker{}}, id
}

func TestWorkerPullReadsLatestInputAndCommitsBeforeAcknowledgment(t *testing.T) {
	app, session := inputQueueFixture(t)
	const answer = "answer before follow-up"
	if _, err := app.store.AppendModelDelta(app.ctx, session, "run", 1, 0, answer); err != nil {
		t.Fatal(err)
	}
	const id = "00000000-0000-4000-8000-000000000056"
	if _, _, err := app.store.QueueInputWithImages(app.ctx, session, id, "later", "original", []store.Image{{MediaType: "image/png", Data: testPNG(t)}}); err != nil {
		t.Fatal(err)
	}
	content, mode := "edited before boundary", "now"
	if _, _, err := app.store.UpdateInput(app.ctx, session, id, &content, &mode); err != nil {
		t.Fatal(err)
	}
	sink := &contextNoticeAckSink{beforeWrite: func(data []byte) error {
		var ack workerproto.Command
		if err := json.Unmarshal(data, &ack); err != nil {
			return err
		}
		if ack.Action != "ack" || ack.Input == nil || ack.Input.Prompt != content || ack.Input.Model.Provider != "" || len(ack.Input.Images) != 1 {
			return fmt.Errorf("incorrect input ACK: %+v", ack)
		}
		inputs, err := app.store.RecentInputs(app.ctx, session)
		if err != nil || len(inputs) != 1 || inputs[0].Status != "delivered" {
			return fmt.Errorf("ACK before queue commit: %+v %v", inputs, err)
		}
		messages, err := app.store.MessagesBefore(app.ctx, session, 0, 10)
		if err != nil || len(messages) != 3 || messages[1].Content != answer || messages[1].OutputTurn != 1 || messages[1].OutputBytes != len(answer) || messages[2].Content != content || messages[2].ImageCount != 1 || ack.Input.MessageSequence != messages[2].Sequence {
			return fmt.Errorf("ACK before message/attachment commit: %+v %v", messages, err)
		}
		if _, _, _, err := app.store.RemoveInput(app.ctx, session, id, true); err != store.ErrInputConsumed {
			return fmt.Errorf("delivery still withdrawable: %v", err)
		}
		return nil
	}}
	w := &worker{cmd: &exec.Cmd{}, input: sink, stopping: true, done: make(chan struct{})}
	app.workers[session] = w
	result, _ := json.Marshal(workerproto.Result{RunID: "run", Kind: "input_poll", AckID: "poll", Reply: &workerproto.CompletedReply{Turn: 1, FullBytes: len(answer), Content: answer}})
	app.readWorkerOutput(session, w, bufio.NewScanner(bytes.NewReader(append(result, '\n'))))
	if sink.Len() == 0 {
		t.Fatal("no acknowledged delivery")
	}
	if input, err := app.pullInterjection(app.ctx, session, "run"); err != nil || input != nil {
		t.Fatalf("duplicate input: %+v %v", input, err)
	}
}

func TestInputQueueHTTPPreservesPauseAttachmentsAndConflicts(t *testing.T) {
	app, session := inputQueueFixture(t)
	const id = "00000000-0000-4000-8000-000000000057"
	if _, _, err := app.store.QueueInputWithImages(app.ctx, session, id, "later", "keep", []store.Image{{MediaType: "image/png", Data: testPNG(t)}}); err != nil {
		t.Fatal(err)
	}
	call := func(method, suffix, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		app.session(w, httptest.NewRequest(method, "/api/sessions/"+session+"/inputs/"+suffix, strings.NewReader(body)))
		return w
	}
	if err := app.store.MarkQueuedInputs(app.ctx, session, "", "interrupted"); err != nil {
		t.Fatal(err)
	}
	if got := call(http.MethodPatch, id, `{"content":"paused edit","mode":"now"}`); got.Code != 200 {
		t.Fatalf("edit: %d %s", got.Code, got.Body.String())
	}
	if input, err := app.pullInterjection(app.ctx, session, "run"); err != nil || input != nil {
		t.Fatalf("paused input delivered: %+v %v", input, err)
	}
	withdraw := call(http.MethodPost, id+"/withdraw", `{}`)
	var draft struct {
		Input  store.QueuedInput `json:"input"`
		Images []struct {
			MediaType string `json:"mediaType"`
			Hash      string `json:"hash"`
		} `json:"images"`
	}
	if withdraw.Code != 200 || json.Unmarshal(withdraw.Body.Bytes(), &draft) != nil || draft.Input.Content != "paused edit" || draft.Input.Status != "withdrawn" || len(draft.Images) != 1 || len(draft.Images[0].Hash) != 64 {
		t.Fatalf("withdraw: %d %s", withdraw.Code, withdraw.Body.String())
	}
	original := httptest.NewRecorder()
	app.session(original, httptest.NewRequest(http.MethodGet, "/api/sessions/"+session+"/resources/"+draft.Images[0].Hash, nil))
	if original.Code != 200 || !bytes.Equal(original.Body.Bytes(), testPNG(t)) {
		t.Fatalf("withdrawn resource: %d %s", original.Code, original.Body.String())
	}
	if item, err := app.store.Get(app.ctx, session); err != nil || item.InputQueuePaused {
		t.Fatalf("withdrawing the last paused input left the queue paused: %+v %v", item, err)
	}
	if got := call(http.MethodDelete, id, ""); got.Code != 409 {
		t.Fatalf("removed input mutated again: %d %s", got.Code, got.Body.String())
	}
	resume := call(http.MethodPost, "resume", `{}`)
	if resume.Code != 200 {
		t.Fatalf("resume: %d %s", resume.Code, resume.Body.String())
	}
	item, err := app.store.Get(app.ctx, session)
	if err != nil || item.InputQueuePaused {
		t.Fatalf("resume state: %+v %v", item, err)
	}
}

func TestNewMessageAfterFailureDoesNotResumeOldInputs(t *testing.T) {
	for _, status := range []string{"failed", "interrupted"} {
		for _, hasPending := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/pending=%v", status, hasPending), func(t *testing.T) {
				app, session := inputQueueFixture(t)
				app.models = modelCatalog{Default: "test", Models: []configuredModel{{Key: "test", Provider: "openai-chat", ID: "test", BaseURL: "http://127.0.0.1:1/v1", APIKey: "mock"}}}
				sink := &imageCommandSink{}
				app.workers[session] = &worker{input: sink}
				if hasPending {
					if _, _, err := app.store.QueueInputWithImages(app.ctx, session, "old", "later", "old pending task", []store.Image{{MediaType: "image/png", Data: testPNG(t)}}); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := app.store.FinishRun(app.ctx, session, "run", "failure", status, "upstream HTTP 403"); err != nil {
					t.Fatal(err)
				}
				item, err := app.store.Get(app.ctx, session)
				if err != nil || item.InputQueuePaused != hasPending {
					t.Fatalf("failure queue state: %+v %v", item, err)
				}

				response := httptest.NewRecorder()
				app.submitMessage(response, httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"content":"try again","modelKey":"test","selectionMode":"manual"}`)), session)
				var sent struct {
					RunID   string             `json:"runId"`
					Message store.Message      `json:"message"`
					Input   *store.QueuedInput `json:"input"`
				}
				if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &sent) != nil || sent.RunID == "" || sent.Input != nil || sent.Message.Content != "try again" {
					t.Fatalf("explicit send did not start a run: %d %s", response.Code, response.Body.String())
				}
				var command workerproto.Command
				if err := json.NewDecoder(&sink.Buffer).Decode(&command); err != nil || command.RunID != sent.RunID || command.Prompt != "try again" {
					t.Fatalf("retry dispatch: %+v %v", command, err)
				}
				if _, err := app.store.FinishRun(app.ctx, session, sent.RunID, "retry-answer", "succeeded", "recovered"); err != nil {
					t.Fatal(err)
				}
				sink.Reset()
				app.launchQueuedInput(session)
				if sink.Len() != 0 {
					t.Fatalf("successful retry automatically dispatched old inputs: %s", sink.String())
				}
				inputs, err := app.store.RecentInputs(app.ctx, session)
				if err != nil || hasPending && (len(inputs) != 1 || inputs[0].ID != "old" || inputs[0].Status != "paused") || !hasPending && len(inputs) != 0 {
					t.Fatalf("retry changed old queue: %+v %v", inputs, err)
				}
				if hasPending {
					if _, err := app.store.ResumeInputs(app.ctx, session); err != nil {
						t.Fatal(err)
					}
					app.launchQueuedInput(session)
					if err := json.NewDecoder(&sink.Buffer).Decode(&command); err != nil || command.Prompt != "old pending task" || len(command.Images) != 1 {
						t.Fatalf("explicit resume lost the old task or attachments: %+v %v", command, err)
					}
				}
			})
		}
	}
}

func TestCancelPersistsQueuePauseBeforeSendingWorkerCommand(t *testing.T) {
	app, session := inputQueueFixture(t)
	if _, _, err := app.store.QueueInputWithImages(app.ctx, session, "image-input", "later", "keep", []store.Image{{MediaType: "image/png", Data: testPNG(t)}}); err != nil {
		t.Fatal(err)
	}
	sink := &contextNoticeAckSink{beforeWrite: func(data []byte) error {
		var command workerproto.Command
		if err := json.Unmarshal(data, &command); err != nil {
			return err
		}
		item, err := app.store.Get(app.ctx, session)
		if err != nil || !item.InputQueuePaused || command.Action != "cancel" {
			return fmt.Errorf("cancel before pause: %+v %+v %v", command, item, err)
		}
		images, err := app.store.QueuedInputImages(app.ctx, session, "image-input")
		if err != nil || len(images) != 1 {
			return fmt.Errorf("cancel deleted image: %+v %v", images, err)
		}
		return nil
	}}
	app.workers[session] = &worker{input: sink, dispatchedRunID: "run"}
	w := httptest.NewRecorder()
	app.session(w, httptest.NewRequest(http.MethodPost, "/api/sessions/"+session+"/cancel", strings.NewReader(`{}`)))
	if w.Code != 202 || sink.Len() == 0 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
}

func TestQueuedRunUsesCurrentSelectionInsteadOfOldSubmissionPreferences(t *testing.T) {
	useExplicitReasoningCatalog(t)
	for _, mode := range []string{"auto", "manual"} {
		t.Run(mode, func(t *testing.T) {
			app, session := inputQueueFixture(t)
			app.models = modelCatalog{Default: "first", Models: []configuredModel{
				{Key: "first", Provider: "openai-chat", ID: "gpt-6-astra", BaseURL: "http://127.0.0.1:1/v1", APIKey: "mock"},
				{Key: "latest", Provider: "openai-chat", ID: "gpt-6-luna", BaseURL: "http://127.0.0.1:1/v1", APIKey: "mock"},
			}}
			speed, quality, first, latest, low, high := "speed", "quality", "first", "latest", "low", "high"
			if _, err := app.store.PatchSessionSelection(app.ctx, session, store.SessionSelectionPatch{SelectionMode: &mode, SelectionTier: &speed, ManualModelKey: &first, ManualReasoningEffort: &low}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := app.store.QueueInput(app.ctx, session, "later", "later", "follow up", first, low, mode, speed); err != nil {
				t.Fatal(err)
			}
			if _, err := app.store.PatchSessionSelection(app.ctx, session, store.SessionSelectionPatch{SelectionTier: &quality, ManualModelKey: &latest, ManualReasoningEffort: &high}); err != nil {
				t.Fatal(err)
			}
			if _, err := app.store.FinishRun(app.ctx, session, "run", "answer", "succeeded", "done"); err != nil {
				t.Fatal(err)
			}
			sink := &imageCommandSink{}
			app.workers[session] = &worker{input: sink}
			app.launchQueuedInput(session)
			var command workerproto.Command
			if err := json.Unmarshal(bytes.TrimSpace(sink.Bytes()), &command); err != nil {
				t.Fatalf("queue not dispatched: %v %s", err, sink.String())
			}
			current, err := app.store.Get(app.ctx, session)
			if err != nil || current.SelectionTier != quality || current.ManualModelKey != latest || current.ManualReasoningEffort != high {
				t.Fatalf("old queue overwrote current preference: %+v %v", current, err)
			}
			if mode == "manual" && (command.Model.Key != latest || command.Model.ReasoningEffort != high) {
				t.Fatalf("queued run ignored current manual selection: %+v", command.Model)
			}
			if mode == "auto" && command.Model.ReasoningEffort != "max" {
				t.Fatalf("automatic queued run not at highest reasoning: %+v", command.Model)
			}
		})
	}
}
