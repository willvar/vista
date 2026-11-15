package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/replylanguage"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestLanguageDeltaDoesNotRestateStaleRulesOrRunFacts(t *testing.T) {
	model := modelcfg.Model{Provider: "openai-responses", ID: "fixture"}
	prefix := &provider.PromptPrefix{Profile: promptPrefixProfile(model), Base: "OLD_RULE", Current: "OLD_RULE", Language: "old locale"}
	messages := []provider.ChatMessage{{Role: "system", Content: prefix.Base, Prefix: prefix}, {Role: "user", Content: "task"}, {Role: "system", Content: "NEW_RULE read during the run"}, {Role: "user", Content: "follow up"}}
	prepared := preparedContext{systemBase: prefix.Base}
	ctx := replylanguage.WithDefault(context.Background(), "ja")
	next, err := prepared.updateLanguage(ctx, workerproto.Command{Model: model}, messages, 2, nil)
	if err != nil || len(next) != len(messages)+1 || next[0].Content != messages[0].Content || !reflect.DeepEqual(next[1:len(messages)], messages[1:]) {
		t.Fatalf("locale change rewrote earlier context: %v", err)
	}
	last := next[len(next)-1]
	if !strings.Contains(last.Content, "Japanese") || strings.Contains(last.Content, "OLD_RULE") || strings.Contains(last.Content, "NEW_RULE") || last.InstructionUpdate || !last.LanguageControl {
		t.Fatal("a language-only delta reasserted global instructions")
	}
	if prefix.Language != "old locale" {
		t.Fatal("language delta mutated canonical prefix metadata")
	}
	again, err := prepared.updateLanguage(ctx, workerproto.Command{Model: model}, next, 3, nil)
	if err != nil || len(again) != len(next) {
		t.Fatal("same locale duplicated its control")
	}
}

func TestRunChecklistIsContextDataNotAnInstructionBaseline(t *testing.T) {
	db, ctx := checkpointFixture(t)
	user, _, err := db.BeginRun(ctx, "session", "run", "user", "task")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReplaceChecklist(ctx, "session", "run", []store.ChecklistItem{{Text: "CURRENT_RUN_FACT", Status: "in_progress"}}); err != nil {
		t.Fatal(err)
	}
	command := workerproto.Command{SessionID: "session", RunID: "run", MessageSequence: user.Sequence, Prompt: "task", Model: modelcfg.Model{Provider: "openai-responses", ID: "fixture"}}
	prepared, err := prepareContext(ctx, command, nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prepared.messages[0].Content, "CURRENT_RUN_FACT") || strings.Contains(prepared.messages[0].Prefix.Current, "CURRENT_RUN_FACT") {
		t.Fatal("run facts entered the stable privileged prefix")
	}
	last := prepared.messages[len(prepared.messages)-1]
	if last.Role != "user" || !last.RuntimeData || !strings.Contains(last.Content, "CURRENT_RUN_FACT") {
		t.Fatal("current run state was lost instead of projected as data")
	}
}
