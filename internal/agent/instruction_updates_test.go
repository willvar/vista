package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/workerproto"
)

func TestInstructionUpdateWaitsForAnUnansweredNativeCallPair(t *testing.T) {
	call := provider.ToolCall{ID: "ask", Type: "function"}
	call.Function.Name, call.Function.Arguments = "question", `{"questions":[]}`
	state := &provider.PromptPrefix{Profile: "route", Base: "old policy", Current: "old policy"}
	messages := []provider.ChatMessage{{Role: "system", Content: state.Base, Prefix: state}, {Role: "user", Content: "task"}, {Role: "assistant", ToolCalls: []provider.ToolCall{call}}}
	waiting, pending := pinPromptPrefix(messages, "new policy", "route")
	if len(pending) != 0 || !reflect.DeepEqual(waiting[len(waiting)-1], messages[len(messages)-1]) || waiting[0].Prefix.Current != "old policy" {
		t.Fatal("idle policy refresh split an unanswered native call")
	}
	answered := append(waiting, provider.ChatMessage{Role: "tool", ToolCallID: "ask", Content: "answered"})
	next, pending := pinPromptPrefix(answered, "new policy", "route")
	if len(pending) != 2 || !next[len(next)-1].InstructionUpdate || !reflect.DeepEqual(next[1:len(answered)], answered[1:]) {
		t.Fatal("answer did not admit the current policy after its complete native pair")
	}
}

func TestSupersededInstructionSnapshotsCanCompactWithoutDroppingCurrentPolicy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Messages []provider.ChatMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		encoded, _ := json.Marshal(request)
		if !strings.Contains(string(encoded), "OBSOLETE_POLICY") || strings.Contains(string(encoded), "ACTIVE_POLICY") {
			t.Error("compaction did not isolate superseded instructions")
		}
		continuationAnswer(w, "Historical policy changed; preserve task findings.")
	}))
	defer upstream.Close()
	command := continuationCommand(upstream.URL, "current task")
	latest := opaqueCompactionGroup("latest")
	messages := []provider.ChatMessage{{Role: "system", Content: "fixed baseline"}, {Role: "user", Content: command.Prompt}, {Role: "system", Content: strings.Repeat("OBSOLETE_POLICY ", 2500), InstructionUpdate: true}}
	messages = append(messages, latest...)
	active := provider.ChatMessage{Role: "system", Content: "ACTIVE_POLICY", InstructionUpdate: true}
	messages = append(messages, active)
	compactor := newExecutionCompactor(messages)
	next, changed, err := compactor.compact(context.Background(), command, messages, nil, func(workerproto.Result) error { return nil }, &runScope{}, 0)
	if err != nil || !changed {
		t.Fatalf("superseded instructions remained permanently pinned: %v", err)
	}
	if !reflect.DeepEqual(next[:2], messages[:2]) || !reflect.DeepEqual(next[len(next)-1], active) {
		t.Fatal("compaction changed the fixed policy or current input")
	}
	for _, message := range next {
		if strings.Contains(message.Content, "OBSOLETE_POLICY") {
			t.Fatal("superseded full snapshot was retained")
		}
	}
}

func TestPromptRoutingProfileDoesNotInvalidateCompatibleOpaqueHistory(t *testing.T) {
	model := modelcfg.Model{Provider: "openai-responses", ProviderID: "custom", ID: "fixture", Key: "gateway", BaseURL: "http://gateway.invalid/v1", ConnectionID: "connection-a"}
	compatible := model
	compatible.ResponsesRoute = modelcfg.ResponsesRouteCodexCompatible
	if promptPrefixProfile(model) == promptPrefixProfile(compatible) {
		t.Fatal("routing profile change did not establish a prompt series")
	}
	if compactionModelKey(model) != compactionModelKey(compatible) {
		t.Fatal("a metadata-only routing choice discarded compatible native replay state")
	}
	other := compatible
	other.ConnectionID = "connection-b"
	if promptPrefixProfile(other) == promptPrefixProfile(compatible) {
		t.Fatal("another connection inherited the same instruction series")
	}
}

func TestCompactionReclaimsLanguageControlsAndRetainsCurrentPolicy(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, snapshotLast := range []bool{false, true} {
			t.Run(fmt.Sprintf("legacy=%t/snapshotLast=%t", legacy, snapshotLast), func(t *testing.T) {
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body struct{ Messages []provider.ChatMessage }
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					for _, message := range body.Messages {
						if strings.Contains(message.Content, "Current reply-language default;") || strings.Contains(message.Content, "ACTIVE_POLICY") {
							t.Error("language controls or current global rules entered the task transcript")
						}
					}
					continuationAnswer(w, "Completed task facts.")
				}))
				defer upstream.Close()
				command := continuationCommand(upstream.URL, "current task")
				command.Action = "compact"
				active := provider.ChatMessage{Role: "system", Content: instructionSnapshotPrefix + "ACTIVE_POLICY\nCurrent snapshot language", InstructionUpdate: true}
				messages := []provider.ChatMessage{{Role: "system", Content: "fixed baseline"}, {Role: "user", Content: command.Prompt}}
				for i := range 12 {
					control := languageControl(fmt.Sprintf("language-%d", i))
					control.LanguageControl = !legacy
					messages = append(messages, provider.ChatMessage{Role: "assistant", Content: strings.Repeat("completed finding ", 500)}, provider.ChatMessage{Role: "user", Content: fmt.Sprintf("follow up %d", i)}, control)
					if !snapshotLast && i == 5 {
						messages = append(messages, active)
					}
				}
				if snapshotLast {
					messages = append(messages, active)
				}
				original := append([]provider.ChatMessage(nil), messages...)
				source := messages
				compactor := newExecutionCompactor(messages)
				for round := range 3 {
					next, changed, err := compactor.compact(context.Background(), command, messages, nil, func(workerproto.Result) error { return nil }, &runScope{}, 0)
					if err != nil || !changed {
						t.Fatalf("round %d did not compact: %v", round, err)
					}
					controls, foundPolicy := 0, false
					for _, message := range next {
						if strings.HasPrefix(message.Content, "Current reply-language default;") {
							controls++
							if !strings.HasSuffix(message.Content, "language-11") {
								t.Fatal("an obsolete language remained active")
							}
						}
						foundPolicy = foundPolicy || reflect.DeepEqual(message, active)
					}
					wantControls := 1
					if snapshotLast {
						wantControls = 0 // The newer complete snapshot supplies the language.
					}
					if controls != wantControls || !foundPolicy || !reflect.DeepEqual(next[0], original[0]) {
						t.Fatalf("round %d: controls=%d want=%d policy retained=%t", round, controls, wantControls, foundPolicy)
					}
					messages = next
				}
				if !reflect.DeepEqual(source, original) {
					t.Fatal("the source history was modified")
				}
			})
		}
	}
}

func TestLanguageControlCleanupIsNotAppliedOnSummaryFailure(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancelled=%t", cancelled), func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if cancelled {
					cancel()
				} else {
					http.Error(w, "fixture summary unavailable", http.StatusBadRequest)
				}
			}))
			defer upstream.Close()
			command := continuationCommand(upstream.URL, "current task")
			command.Action = "compact"
			messages := []provider.ChatMessage{{Role: "system", Content: "baseline"}, {Role: "user", Content: command.Prompt}, languageControl("old"), {Role: "assistant", Content: strings.Repeat("completed facts ", 500)}, languageControl("current")}
			original := append([]provider.ChatMessage(nil), messages...)
			compactor := newExecutionCompactor(messages)
			next, changed, err := compactor.compact(ctx, command, messages, nil, func(workerproto.Result) error { return nil }, &runScope{}, 0)
			if err == nil || changed || !reflect.DeepEqual(next, original) || !reflect.DeepEqual(messages, original) || compactor.summary != "" {
				t.Fatalf("failed summary changed usable language state: changed=%t err=%v", changed, err)
			}
		})
	}
}
