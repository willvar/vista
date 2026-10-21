package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"vista/internal/provider"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

func TestShellDisplayRedactsCredentials(t *testing.T) {
	for _, test := range []struct {
		name, command, want string
	}{
		{"environment", `OPENAI_API_KEY=env-secret TOKEN='token with spaces' PASSWORD="password secret" curl https://example.test`, `OPENAI_API_KEY=[redacted] TOKEN=[redacted] PASSWORD=[redacted] curl https://example.test`},
		{"headers", `curl -H 'Authorization: Bearer header-secret' -H "x-api-key: other-secret" https://example.test`, `curl -H 'Authorization: Bearer [redacted]' -H "x-api-key: [redacted]" https://example.test`},
		{"basic", `curl -H 'Authorization: Basic abc123==' https://example.test`, `curl -H 'Authorization: Basic [redacted]' https://example.test`},
		{"options", `client --api-key flag-secret --token 'token secret' --password="password secret"`, `client --api-key [redacted] --token [redacted] --password=[redacted]`},
		{"json", `curl -d '{"apiKey":"json-secret","client_secret":"json-password"}'`, `curl -d '{"apiKey":[redacted],"client_secret":[redacted]}'`},
		{"url", `curl 'https://username:password@example.test/?token=url-secret&format=json'`, `curl 'https://[redacted]@example.test/?token=[redacted]&format=json'`},
		{"user", `curl --user 'name:password' https://example.test`, `curl --user [redacted] https://example.test`},
		{"ordinary", `git status --short && printf 'hello world'`, `git status --short && printf 'hello world'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := tools.DisplayShellCommand(test.command, nil); got != test.want {
				t.Fatalf("redacted command=%q, want %q", got, test.want)
			}
		})
	}
	if got := tools.DisplayShellCommand("printf known-config-key", []string{"known-config-key"}); got != "printf [redacted]" {
		t.Fatalf("unlabelled configured credential was exposed: %q", got)
	}
}

func TestToolInputOnlyStoresShellMetadata(t *testing.T) {
	for _, test := range []struct {
		name, arguments string
		timeout         int
	}{
		{"shell", `{"directory":"/work","command":"git status"}`, 60},
		{"shell", `{"directory":"/work","command":"git status","timeoutSeconds":30}`, 30},
		{"start_background", `{"directory":"/work","command":"npm run dev"}`, 3600},
		{"read_file", `{"path":"/work/file","command":"not displayable"}`, 0},
		{"mcp_call_tool", `{"arguments":{"password":"private"}}`, 0},
	} {
		t.Run(fmt.Sprintf("%s/%d", test.name, test.timeout), func(t *testing.T) {
			var call provider.ToolCall
			call.Function.Name, call.Function.Arguments = test.name, test.arguments
			input := toolInput(call, workerproto.Command{})
			if test.timeout == 0 {
				if input != nil {
					t.Fatalf("non-shell arguments leaked: %+v", input)
				}
				return
			}
			if input == nil || input.TimeoutSeconds != test.timeout || input.Directory != "/work" || input.Command == "" {
				t.Fatalf("shell display input missing: %+v", input)
			}
		})
	}
}

func TestShellEventsKeepDisplayMetadataWithoutChangingExecution(t *testing.T) {
	root, _ := continuationFixture(t)
	command := "DISPLAY_TOKEN=fixture-secret; test \"$DISPLAY_TOKEN\" = fixture-secret && printf success"
	arguments, _ := json.Marshal(map[string]any{"directory": root, "command": command, "timeoutSeconds": 5})
	var requests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			continuationEvent(w, mcpToolEvent("shell-display", "shell", string(arguments)))
			return
		}
		continuationAnswer(w, "complete")
	}))
	defer model.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var toolEvents []workerproto.Result
	runCommand := continuationCommand(model.URL, "execute fixture")
	runCommand.Model.APIKey = "fixture-secret"
	answer, err := Run(ctx, runCommand, func(event workerproto.Result) error {
		if event.Kind == "tool" {
			toolEvents = append(toolEvents, event)
		}
		return nil
	})
	if err != nil || answer != "complete" || len(toolEvents) != 2 {
		t.Fatalf("shell flow: answer=%q events=%d err=%v", answer, len(toolEvents), err)
	}
	for _, event := range toolEvents {
		input := event.Tool.Input
		if input == nil || input.Directory != root || input.TimeoutSeconds != 5 || !strings.Contains(input.Command, "DISPLAY_TOKEN=[redacted]") {
			t.Fatalf("shell event lost display metadata: %+v", event)
		}
		if strings.Contains(input.Command, "fixture-secret") {
			t.Fatalf("shell event leaked configured credential: %q", input.Command)
		}
	}
	if toolEvents[0].Status != "started" || toolEvents[1].Status != "succeeded" || !strings.Contains(toolEvents[1].Content, "success") {
		t.Fatalf("display redaction changed shell execution: %+v", toolEvents)
	}
}

func TestLongShellDisplayFitsResultFrameWithoutChangingArguments(t *testing.T) {
	command := "printf configured-secret " + strings.Repeat("<\"🙂\n\t", 6000)
	directory := "/work/" + strings.Repeat("<\"🙂\n\t", 3000)
	arguments, _ := json.Marshal(map[string]any{"command": command, "directory": directory, "timeoutSeconds": 5})
	for _, name := range []string{"shell", "start_background"} {
		t.Run(name, func(t *testing.T) {
			call := scriptedToolCall("long-command", name, string(arguments))
			config := workerproto.Command{}
			config.Model.APIKey = "configured-secret"
			input := toolInput(call, config)
			if input == nil || input.TimeoutSeconds != 5 || !utf8.ValidString(input.Command+input.Directory) || strings.Contains(input.Command, "configured-secret") {
				t.Fatalf("invalid or unredacted display: %+v", input)
			}
			for _, value := range []string{input.Command, input.Directory} {
				if !strings.Contains(value, "Display truncated;") {
					t.Fatal("long argument was silently truncated")
				}
			}
			result := workerproto.Result{RunID: "run", TaskID: strings.Repeat("task/", 200), Depth: 2, Turn: 1, Kind: "tool", Status: "failed",
				Content: eventPreview(strings.Repeat("<\"🙂\n\t", 6000)), Tool: &workerproto.ToolProgress{CallID: "long-command", Name: name, Summary: toolSummary(call), Input: input}}
			encoded, err := json.Marshal(result)
			if err != nil || len(encoded) > workerproto.MaxResultFrame {
				t.Fatalf("display metadata overflowed the result frame: bytes=%d err=%v", len(encoded), err)
			}
			if call.Function.Arguments != string(arguments) {
				t.Fatal("display truncation changed the actual tool arguments")
			}
		})
	}
}
