package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vista/internal/question"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestQuestionReplyUsesMessageSelectionAndPreservesPendingOnRejection(t *testing.T) {
	app, session := inputQueueFixture(t)
	questions := []question.Question{{Question: "Which path?", Options: []question.Option{{Label: "src"}, {Label: "web"}}}}
	pending := &store.PendingReply{CallID: "ask", Kind: "question", Content: question.Format(questions), Questions: questions}
	if _, err := app.store.FinishRunWithPending(context.Background(), session, "run", "question", "succeeded", pending.Content, pending); err != nil {
		t.Fatal(err)
	}
	app.models = modelCatalog{Default: "original", Models: []configuredModel{
		{Key: "original", Provider: "openai-chat", ID: "original-model", BaseURL: "http://127.0.0.1:1/v1", APIKey: "key"},
		{Key: "selected", Provider: "openai-chat", ID: "gpt-6-luna", BaseURL: "http://127.0.0.1:1/v1", APIKey: "key"},
	}}
	request := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		app.submitMessage(w, httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(body)), session)
		return w
	}
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"questionReply":{"runId":"run","answers":[]}}`, 400},
		{`{"questionReply":{"runId":"run","answers":[{"options":["unknown"]}]}}`, 400},
		{`{"questionReply":{"runId":"stale","answers":[{"options":["src"]}]}}`, 409},
		{`{"questionReply":{"runId":"run","answers":[{"options":["src"]}]}}`, 503},
	} {
		w := request(tc.body)
		if w.Code != tc.status {
			t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
		}
		item, err := app.store.Get(app.ctx, session)
		if err != nil || item.Pending == nil || item.Pending.RunID != "run" {
			t.Fatalf("rejected answer consumed pending: %+v %v", item, err)
		}
	}
	sink := &imageCommandSink{}
	app.workers[session] = &worker{input: sink}
	body := `{"modelKey":"selected","selectionMode":"manual","reasoningEffort":"high","questionReply":{"runId":"run","answers":[{"options":["src"]}]}}`
	w := request(body)
	if w.Code != 202 {
		t.Fatalf("answer status=%d body=%s", w.Code, w.Body.String())
	}
	var command workerproto.Command
	if err := json.NewDecoder(bytes.NewReader(sink.Bytes())).Decode(&command); err != nil {
		t.Fatal(err)
	}
	if command.QuestionCallID != pending.CallID || command.Model.ID != "gpt-6-luna" || command.Model.ReasoningEffort != "high" || command.Prompt != "1. Which path?\n\nsrc" {
		t.Fatalf("selection/answer not dispatched: %+v", command)
	}
	previous, err := app.store.NextHistoryMessage(context.Background(), session, 0, command.MessageSequence)
	if err != nil || previous.Role != "user" {
		t.Fatalf("durable history missing: %+v %v", previous, err)
	}
	question, err := app.store.NextHistoryMessage(context.Background(), session, previous.Sequence, command.MessageSequence)
	if err != nil || question.Content != pending.Content {
		t.Fatalf("question missing from durable history: %+v %v", question, err)
	}

	if w := request(body); w.Code != 409 {
		t.Fatalf("double reply status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestQuestionWorkerResultPersistsChoicesWithoutLaunchingQueue(t *testing.T) {
	consumer, _ := newResultConsumerTest(t)
	consumer.finished = func() { t.Fatal("question automatically launched queued input") }
	questions := []question.Question{{Question: "Which path?", Options: []question.Option{{Label: "src"}}}}
	content := question.Format(questions)
	err := consumeTestResult(t, consumer, workerproto.Result{RunID: "run", Status: "succeeded", Content: content, Pending: &workerproto.PendingReply{CallID: "ask", Kind: "question", Content: content, Questions: questions}})
	if err != nil {
		t.Fatal(err)
	}
	item, err := consumer.store.Get(consumer.ctx, "session")
	if err != nil || item.Pending == nil || item.Pending.CallID != "ask" || len(item.Pending.Questions) != 1 || item.Pending.Content != content {
		t.Fatalf("question worker persistence: %+v %v", item, err)
	}
}
