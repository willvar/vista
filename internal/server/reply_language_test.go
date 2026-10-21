package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
	"vista/internal/workerproto"
)

func TestReplyLanguageReachesRunDispatchAndQueuedInterjection(t *testing.T) {
	app, session := inputQueueFixture(t)
	save := func(code string) bool {
		r := httptest.NewRequest("POST", "/messages", nil)
		r.Header.Set("X-Vista-Language", code)
		return app.saveReplyLanguage(httptest.NewRecorder(), r, session)
	}
	for _, code := range []string{"zh-CN", "zh-HK", "zh-MO", "zh-SG", "zh-MY", "zh-TW", "en-GB", "es-MX", "pt-PT"} {
		if !save(code) {
			t.Fatalf("could not save %s", code)
		}
	}
	if save("unknown") {
		t.Fatal("language validation")
	}
	// Use the actual initial message sequence rather than an event assumption.
	messages, err := app.store.MessagesBefore(app.ctx, session, 0, 10)
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages: %+v %v", messages, err)
	}
	message := messages[0]
	sink := &imageCommandSink{}
	app.workers[session] = &worker{input: sink}
	if err := app.dispatchRun(app.ctx, session, "run", message, modelcfg.Model{ID: "model"}); err != nil {
		t.Fatal(err)
	}
	var command workerproto.Command
	if err := json.NewDecoder(sink).Decode(&command); err != nil || command.ReplyLanguage != "pt-PT" {
		t.Fatalf("dispatch: %+v %v", command, err)
	}
	if _, _, err := app.store.QueueInput(app.ctx, session, "queued", "now", "Continue"); err != nil {
		t.Fatal(err)
	}
	if !save("de-CH") {
		t.Fatal("could not change language")
	}
	input, err := app.pullInterjection(app.ctx, session, "run")
	if err != nil || input == nil || input.ReplyLanguage != "de-CH" || input.Prompt != "Continue" {
		t.Fatalf("interjection: %+v %v", input, err)
	}
	r := httptest.NewRequest("POST", "/messages", nil)
	if !app.saveReplyLanguage(httptest.NewRecorder(), r, session) {
		t.Fatal("missing header should preserve preference")
	}
	if code, err := app.store.ReplyLanguage(app.ctx, session); err != nil || !strings.EqualFold(code, "de-CH") {
		t.Fatalf("preserved: %s %v", code, err)
	}
}
