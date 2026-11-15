package agent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"vista/internal/agentconfig"
	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/replylanguage"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestQueuedLanguageUpdatesBeforeFirstModelRequestRetainHistory(t *testing.T) {
	db, ctx := checkpointFixture(t)
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		historyTextResponse(w, "done")
	}))
	defer upstream.Close()
	user, _, err := db.BeginRun(ctx, "session", "locale-run", "first-input", "first")
	if err != nil {
		t.Fatal(err)
	}
	command := workerproto.Command{SessionID: "session", RunID: "locale-run", MessageSequence: user.Sequence, Prompt: user.Content, ReplyLanguage: "en",
		Model: modelcfg.Model{Provider: "openai-chat", ID: "fixture", APIKey: "test", BaseURL: upstream.URL}}
	inputs := []struct{ id, language string }{{"second-input", "zh-CN"}, {"third-input", "en"}}
	for _, input := range inputs {
		if _, _, err := db.QueueInput(ctx, "session", input.id, input.id); err != nil {
			t.Fatal(err)
		}
	}
	next := func(*workerproto.CompletedReply) (*workerproto.Command, error) {
		if len(inputs) == 0 {
			return nil, nil
		}
		input := inputs[0]
		inputs = inputs[1:]
		message, err := db.DeliverInput(ctx, "session", command.RunID, input.id, input.id+"-message")
		if err != nil {
			return nil, err
		}
		return &workerproto.Command{RunID: command.RunID, InputID: input.id, MessageSequence: message.Sequence, Prompt: message.Content, ReplyLanguage: input.language}, nil
	}
	var final store.ModelOutputRef
	sink := historyTestSink(db, command, &final)
	prefixes := 0
	emit := func(result workerproto.Result) error {
		if err := sink(result); err != nil {
			return err
		}
		if result.Kind == "model_history" && result.Status == "prefix" {
			prefixes++
			return sink(result) // Retransmitted ACK frames must remain idempotent.
		}
		return nil
	}
	answer, err := RunWithInputSource(ctx, command, emit, next, nil)
	if err != nil || answer != "done" || requests.Load() != 1 || prefixes != 3 {
		t.Fatalf("queued locale changes: answer=%q requests=%d prefixes=%d err=%v", answer, requests.Load(), prefixes, err)
	}
	entries, err := db.ContextEntries(ctx, "session", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	retained := 0
	for _, entry := range entries {
		if entry.History != nil && entry.History.Status == "prefix" {
			retained++
		}
	}
	if retained != 3 {
		t.Fatalf("retained prefix count = %d", retained)
	}
	if _, err := db.FinishRun(ctx, "session", command.RunID, "reply", "succeeded", answer, final); err != nil {
		t.Fatal(err)
	}
	user, _, err = db.BeginRun(ctx, "session", "next-run", "next-input", "continue")
	if err != nil {
		t.Fatal(err)
	}
	command.RunID, command.MessageSequence, command.Prompt = "next-run", user.Sequence, user.Content
	messages, prefix, err := loadConversationFrame(ctx, command, 0)
	if err != nil || prefix == nil || prefix.Language != replylanguage.Instruction(replylanguage.WithDefault(ctx, "en")) {
		t.Fatalf("latest locale was not restored: prefix=%+v err=%v", prefix, err)
	}
	controls := 0
	for _, message := range messages {
		if strings.HasPrefix(message.Content, "Current reply-language default;") {
			controls++
		}
	}
	if controls != 2 {
		t.Fatalf("restored language controls = %d", controls)
	}
}

func runtimeNoticeOnWire(role, content string) bool {
	return role == "user" && strings.HasPrefix(content, "Runtime completion event.")
}

func TestPromptPrefixPreservesBaselineAndAppendsAdmittedChanges(t *testing.T) {
	initial := []provider.ChatMessage{{Role: "system", Content: "policy-a"}, {Role: "user", Content: "first"}}
	first, marker := pinPromptPrefix(initial, "policy-a", "route-a")
	if len(marker) != 1 || marker[0].Role != "" || first[0].Content != "policy-a" {
		t.Fatal("initial series not established")
	}
	// This is the exact persisted chronology: input, metadata, completed reply.
	history := append(append([]provider.ChatMessage{initial[0], initial[1]}, marker...), provider.ChatMessage{Role: "assistant", Content: "done"}, provider.ChatMessage{Role: "user", Content: "second"})
	next, pending := pinPromptPrefix(history, "policy-b", "route-a")
	if next[0].Content != "policy-a" || len(pending) != 2 || next[len(next)-1].Role != "system" || !strings.HasSuffix(next[len(next)-1].Content, "policy-b") {
		t.Fatal("changed rules rewrote baseline or lost privileged update")
	}
	if !reflect.DeepEqual(first[1:], next[1:2]) {
		t.Fatal("earlier input was rewritten")
	}
	if history[0].Content != "policy-a" || initial[0].Prefix != nil {
		t.Fatal("canonical input was mutated")
	}
	resumed := append([]provider.ChatMessage{next[0]}, next[1:]...)
	resumed = append(resumed, pending[0])
	again, duplicate := pinPromptPrefix(resumed, "policy-b", "route-a")
	if len(duplicate) != 0 || len(again) != len(next) {
		t.Fatal("replay duplicated admitted instructions")
	}
	changed, pending := pinPromptPrefix(resumed, "policy-c", "route-b")
	if changed[0].Content != "policy-c" || len(pending) != 2 {
		t.Fatal("changed route retained an incompatible baseline")
	}
}

func TestPromptPrefixLedgerRestoresAcrossRunsAndCheckpoint(t *testing.T) {
	db, ctx := checkpointFixture(t)
	model := modelcfg.Model{Provider: "openai-responses", ID: "fixture", BaseURL: "http://gateway.invalid/v1"}
	user, _, err := db.BeginRun(ctx, "session", "first-run", "input-a", "first")
	if err != nil {
		t.Fatal(err)
	}
	command := workerproto.Command{SessionID: "session", RunID: "first-run", MessageSequence: user.Sequence, Prompt: "first", Model: model}
	prepared, err := prepareContext(ctx, command, nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	base := prepared.messages[0].Content
	persist := func(result workerproto.Result) error {
		_, err := db.RecordModelHistory(ctx, "session", result)
		return err
	}
	if err := retainPromptPrefix(command, 1, prepared.prefix, persist); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRun(ctx, "session", command.RunID, "reply-a", "succeeded", "done"); err != nil {
		t.Fatal(err)
	}
	user, _, err = db.BeginRun(ctx, "session", "second-run", "input-b", "second")
	if err != nil {
		t.Fatal(err)
	}
	command.RunID, command.MessageSequence, command.Prompt = "second-run", user.Sequence, "second"
	prepared, err = prepareContext(ctx, command, nil, 0, 0)
	if err != nil || prepared.messages[0].Content != base || len(prepared.prefix) != 0 {
		t.Fatalf("baseline replay: %v", err)
	}
	// Use the same configuration root as checkpointFixture, without opening live state.
	config := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "vista")
	if err := agentconfig.Write(config, "changed policy"); err != nil {
		t.Fatal(err)
	}
	prepared, err = prepareContext(ctx, command, nil, 0, 0)
	if err != nil || prepared.messages[0].Content != base || len(prepared.prefix) != 2 {
		t.Fatalf("rule update replay: %v", err)
	}
	if err := retainPromptPrefix(command, 1, prepared.prefix, persist); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := saveContextCheckpoint(ctx, command, prepared.messages)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(filepath.Join(config, "state", "outputs"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	loaded, err := loadContextFile(root, checkpoint.Path, command)
	if err != nil {
		t.Fatal(err)
	}
	restored, pending := pinPromptPrefix(append([]provider.ChatMessage{{Role: "system"}}, loaded...), prepared.messages[0].Prefix.Current, promptPrefixProfile(model))
	if len(pending) != 0 || restored[0].Content != base || strings.Count(restored[len(restored)-1].Content, "changed policy") != 1 {
		t.Fatal("checkpoint lost or duplicated instruction state")
	}
}
