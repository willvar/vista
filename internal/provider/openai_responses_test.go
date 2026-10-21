package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	modelcfg "vista/internal/model"
)

func TestOpenAIResponsesTextAndFunctionRoundTrip(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("wrong Responses route or authorization: %s", r.URL)
		}
		var request struct {
			Model        string           `json:"model"`
			Stream       bool             `json:"stream"`
			Store        bool             `json:"store"`
			Instructions string           `json:"instructions"`
			Input        []map[string]any `json:"input"`
			Tools        []map[string]any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "test-model" || !request.Stream || request.Store || request.Instructions != "You are helpful" {
			t.Errorf("wrong Responses request controls: %+v", request)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			if len(request.Tools) != 1 || request.Tools[0]["name"] != "read_file" || request.Tools[0]["type"] != "function" {
				t.Errorf("function schema missing: %+v", request.Tools)
			}
			fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read_file\",\"arguments\":\"\"}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":\"{\\\"path\\\":\"}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":\"\\\"/tmp/a\\\"}\"}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"fc_1\",\"arguments\":\"{\\\"path\\\":\\\"/tmp/a\\\"}\"}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\\\"/tmp/a\\\"}\"}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
			return
		}
		if len(request.Input) != 3 || request.Input[1]["type"] != "function_call" || request.Input[2]["type"] != "function_call_output" || request.Input[2]["call_id"] != "call_1" {
			t.Errorf("Responses tool history was not lowered: %+v", request.Input)
		}
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_2\",\"delta\":\"Read complete\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "openai-responses", ID: "test-model", BaseURL: server.URL + "/v1", APIKey: "test-key"}
	messages := []ChatMessage{{Role: "system", Content: "You are helpful"}, {Role: "user", Content: "read file"}}
	schema := map[string]any{"type": "function", "function": map[string]any{"name": "read_file", "description": "Read a file", "parameters": map[string]any{"type": "object"}}}
	turn, err := OpenAIResponsesTurn(context.Background(), model, messages, []map[string]any{schema})
	if err != nil || len(turn.Calls) != 1 || turn.Calls[0].ID != "call_1" || turn.Calls[0].Function.Arguments != `{"path":"/tmp/a"}` {
		t.Fatalf("Responses function turn: %+v %v", turn, err)
	}
	messages = append(messages, ChatMessage{Role: "assistant", ToolCalls: turn.Calls}, ChatMessage{Role: "tool", ToolCallID: "call_1", Content: "file contents"})
	turn, err = OpenAIResponsesTurn(context.Background(), model, messages, []map[string]any{schema})
	if err != nil || turn.Text != "Read complete" || calls != 2 {
		t.Fatalf("Responses text turn: %+v %v calls=%d", turn, err, calls)
	}
}

func TestOpenAIResponsesStreamsWithoutContentType(t *testing.T) {
	for _, check := range []struct {
		name string
		body string
		ok   bool
	}{
		{"SSE without header", "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"Hi\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n", true},
		{"JSON without header", `{"error":"not a stream"}`, false},
	} {
		t.Run(check.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "")
				_, _ = w.Write([]byte(check.body))
			}))
			defer server.Close()
			turn, err := OpenAIResponsesTurn(context.Background(), modelcfg.Model{Provider: "openai-responses", ID: "gpt-6-luna", BaseURL: server.URL + "/v1", APIKey: "key"}, []ChatMessage{{Role: "user", Content: "Hi"}}, nil)
			if check.ok && (err != nil || turn.Text != "Hi") || !check.ok && (err == nil || !strings.Contains(err.Error(), "content type")) {
				t.Fatalf("missing content type: %+v %v", turn, err)
			}
		})
	}
}

func TestResponsesHostedToolResultsAndLimits(t *testing.T) {
	completed := func(output []any) string {
		event, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": output}})
		return "data: " + string(event) + "\n\n"
	}
	output := []any{
		map[string]any{"type": "mcp_call", "id": "mcp_1", "status": "completed", "result": map[string]any{"name": "fixture"}},
		map[string]any{"type": "file_search_call", "id": "file_1", "status": "failed", "error": "secret-provider-error"},
	}
	turn, err := parseResponses(strings.NewReader(completed(output)))
	if err != nil || len(turn.HostedCalls) != 2 || turn.HostedCalls[0].Name != "mcp" || turn.HostedCalls[0].Failure || !strings.Contains(turn.HostedCalls[0].Result, "fixture") || turn.HostedCalls[1].Name != "file_search" || !turn.HostedCalls[1].Failure || strings.Contains(turn.HostedCalls[1].Result, "secret-provider-error") {
		t.Fatalf("hosted results: %+v, err=%v", turn.HostedCalls, err)
	}
	for _, check := range []struct {
		name   string
		output []any
	}{
		{"large item", []any{map[string]any{"type": "mcp_call", "id": "large", "result": strings.Repeat("x", maxHostedResultBytes)}}},
		{"event encoding", []any{map[string]any{"type": "mcp_call", "id": "escaped", "result": strings.Repeat("<", 3<<10)}}},
		{"aggregate", func() []any {
			items := make([]any, 5)
			for i := range items {
				items[i] = map[string]any{"type": "mcp_call", "id": fmt.Sprintf("item_%d", i), "result": strings.Repeat("x", 7<<10)}
			}
			return items
		}()},
		{"duplicate ID", []any{map[string]any{"type": "mcp_call", "id": "same"}, map[string]any{"type": "file_search_call", "id": "same"}}},
		{"image output", []any{map[string]any{"type": "image_generation_call", "id": "image_1", "result": "aGVsbG8="}}},
	} {
		t.Run(check.name, func(t *testing.T) {
			if _, err := parseResponses(strings.NewReader(completed(check.output))); err == nil {
				t.Fatal("unsupported or oversized hosted output was accepted")
			}
		})
	}
	stream := `data: {"type":"response.output_item.done","item":{"type":"mcp_call","id":"missing"}}` + "\n\n" + completed(nil)
	if _, err := parseResponses(strings.NewReader(stream)); err == nil || !strings.Contains(err.Error(), "omits a hosted tool item") {
		t.Fatalf("completed response silently omitted a streamed hosted result: %v", err)
	}
	var added strings.Builder
	hostedOutput := make([]any, 32)
	for i := range hostedOutput {
		fmt.Fprintf(&added, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"mcp_call\",\"id\":\"item_%d\"}}\n\n", i)
		hostedOutput[i] = map[string]any{"type": "mcp_call", "id": fmt.Sprintf("item_%d", i)}
	}
	if turn, err := parseResponses(strings.NewReader(added.String() + completed(hostedOutput))); err != nil || len(turn.HostedCalls) != len(hostedOutput) {
		t.Fatalf("hosted items incorrectly limited by execution concurrency: calls=%d, err=%v", len(turn.HostedCalls), err)
	}
}

func TestOpenAIResponsesPreservesUsageAndFinishReason(t *testing.T) {
	stream := "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg\",\"delta\":\"done\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":42,\"output_tokens\":17,\"input_tokens_details\":{\"cached_tokens\":13},\"output_tokens_details\":{\"reasoning_tokens\":5}}}}\n\n"
	turn, err := parseResponses(strings.NewReader(stream))
	if err != nil || turn.Text != "done" || turn.FinishReason != "completed" || turn.Usage == nil || *turn.Usage != (ModelUsage{InputTokens: 42, OutputTokens: 17, CacheReadTokens: 13, ReasoningTokens: 5}) {
		t.Fatalf("OpenAI Responses result metadata: %+v, err=%v", turn, err)
	}
}

func TestResponsesURLCitationsFromStreamAndCompletedOutput(t *testing.T) {
	stream := `data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"Cited answer"}` + "\n\n" +
		`data: {"type":"response.output_text.annotation.added","item_id":"msg_1","annotation":{"type":"url_citation","url":"https://example.com/one"}}` + "\n\n" +
		`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Cited answer","annotations":[{"type":"url_citation","url":"https://example.com/one"},{"type":"url_citation","url":"http://example.org/two"},{"type":"file_citation","file_id":"file_1"}]}]}]}}` + "\n\n"
	turn, err := parseResponses(strings.NewReader(stream))
	if err != nil || turn.Text != "Cited answer" || len(turn.Sources) != 2 || turn.Sources[0] != "https://example.com/one" || turn.Sources[1] != "http://example.org/two" {
		t.Fatalf("Responses URL citations: %+v, err=%v", turn, err)
	}
	for _, check := range []struct {
		name, stream string
	}{
		{"missing item ID", `data: {"type":"response.output_text.annotation.added","annotation":{"type":"url_citation","url":"https://example.com"}}` + "\n\n"},
		{"unsafe URL", `data: {"type":"response.output_text.annotation.added","item_id":"msg","annotation":{"type":"url_citation","url":"javascript:alert(1)"}}` + "\n\n"},
		{"missing URL", `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[{"type":"url_citation"}]}]}]}}` + "\n\n"},
	} {
		t.Run(check.name, func(t *testing.T) {
			if _, err := parseResponses(strings.NewReader(check.stream)); err == nil {
				t.Fatal("invalid Responses citation accepted")
			}
		})
	}
}

func TestResponsesCitationsBeyondLimitDoNotFailStream(t *testing.T) {
	var stream strings.Builder
	for i := range 20 {
		fmt.Fprintf(&stream, "data: {\"type\":\"response.output_text.annotation.added\",\"item_id\":\"msg\",\"annotation\":{\"type\":\"url_citation\",\"url\":\"https://example.com/source-%02d\"}}\n\n", i)
	}
	stream.WriteString(`data: {"type":"response.completed","response":{"status":"completed","output":[]}}` + "\n\n")

	turn, err := parseResponses(strings.NewReader(stream.String()))
	if err != nil || len(turn.Sources) != 16 || turn.Sources[0] != "https://example.com/source-00" || turn.Sources[15] != "https://example.com/source-15" {
		t.Fatalf("citation overflow failed instead of truncating: sources=%d err=%v", len(turn.Sources), err)
	}
}

func TestResponsesToolCallsAreNotLimitedByExecutionConcurrency(t *testing.T) {
	for _, count := range []int{5, 9, 32} {
		t.Run(fmt.Sprintf("calls-%d", count), func(t *testing.T) {
			output := make([]map[string]any, count)
			for index := range output {
				output[index] = map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%d", index), "call_id": fmt.Sprintf("call_%d", index), "name": "read_file", "arguments": "{}"}
			}
			event, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": output}})
			turn, err := parseResponses(strings.NewReader("data: " + string(event) + "\n\n"))
			if err != nil || len(turn.Calls) != count {
				t.Fatalf("Responses lost %d calls: %+v, err=%v", count, turn, err)
			}
		})
	}
}

func TestResponsesToolCallFragmentsHaveAnAggregateByteLimit(t *testing.T) {
	var stream strings.Builder
	for index := 0; index < 32; index++ {
		item := map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%d", index), "call_id": fmt.Sprintf("call_%d", index), "name": "read_file", "arguments": ""}
		added, _ := json.Marshal(map[string]any{"type": "response.output_item.added", "item": item})
		delta, _ := json.Marshal(map[string]any{"type": "response.function_call_arguments.delta", "item_id": item["id"], "delta": `{"text":"` + strings.Repeat("x", 8192) + `"}`})
		fmt.Fprintf(&stream, "data: %s\n\ndata: %s\n\n", added, delta)
	}
	if _, err := parseResponses(strings.NewReader(stream.String())); err == nil || !strings.Contains(err.Error(), "tool data exceeds 256 KiB") {
		t.Fatalf("oversized aggregate tool fragments accepted: %v", err)
	}
}

func TestOpenAIResponsesStreamsVisibleReasoning(t *testing.T) {
	stream := `data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","delta":"Summary. "}` + "\n\n" +
		`data: {"type":"response.reasoning_text.delta","item_id":"rs_1","delta":"Detail."}` + "\n\n" +
		`data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"Answer."}` + "\n\n" +
		`data: {"type":"response.completed","response":{"status":"completed","output":[]}}` + "\n\n"
	var text, reasoning strings.Builder
	turn, err := parseResponsesStreamWithReasoning(strings.NewReader(stream), func(fragment string) error { _, err := text.WriteString(fragment); return err }, func(fragment string) error { _, err := reasoning.WriteString(fragment); return err })
	if err != nil || turn.Text != "Answer." || text.String() != "Answer." || reasoning.String() != "Summary. Detail." {
		t.Fatalf("Responses reasoning: turn=%+v, text=%q, reasoning=%q, err=%v", turn, text.String(), reasoning.String(), err)
	}
	if _, err := parseResponses(strings.NewReader(stream)); err == nil || !strings.Contains(err.Error(), "requires a sink") {
		t.Fatalf("reasoning was silently dropped: %v", err)
	}
	abort := errors.New("reasoning persistence failed")
	if _, err := parseResponsesStreamWithReasoning(strings.NewReader(stream), nil, func(string) error { return abort }); !errors.Is(err, abort) {
		t.Fatalf("reasoning sink error was not propagated: %v", err)
	}
}

func TestOpenAIResponsesTerminalReasoning(t *testing.T) {
	event := func(value any) string {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return "data: " + string(raw) + "\n\n"
	}
	summary := func(id string, parts ...string) map[string]any {
		texts := make([]map[string]string, 0, len(parts))
		for _, text := range parts {
			texts = append(texts, map[string]string{"type": "summary_text", "text": text})
		}
		return map[string]any{"type": "reasoning", "id": id, "summary": texts}
	}
	completed := func(items ...any) string {
		return event(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": items}})
	}
	done := func(id string, index int, text string) string {
		return event(map[string]any{"type": "response.reasoning_summary_text.done", "item_id": id, "summary_index": index, "text": text})
	}
	delta := func(id string, index int, text string) string {
		return event(map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": id, "summary_index": index, "delta": text})
	}
	itemDone := func(item any) string { return event(map[string]any{"type": "response.output_item.done", "item": item}) }
	for _, check := range []struct{ name, stream, want string }{
		{"text done only", done("r", 0, "Visible summary.") + completed(), "Visible summary."},
		{"item done only", itemDone(summary("r", "Visible summary.")) + completed(), "Visible summary."},
		{"completed only", completed(summary("r", "Visible summary.")), "Visible summary."},
		{"all terminal echoes", delta("r", 0, "Visible ") + done("r", 0, "Visible summary.") + itemDone(summary("r", "Visible summary.")) + completed(summary("r", "Visible summary.")), "Visible summary."},
		{"partially streamed", delta("r", 0, "Visible ") + completed(summary("r", "Visible summary.")), "Visible summary."},
		{"parts and items", delta("a", 0, "Same.") + done("a", 0, "Same.") + done("a", 1, "Same.") + itemDone(summary("a", "Same.", "Same.")) + completed(summary("a", "Same.", "Same."), summary("b", "Same.")), "Same.Same.Same."},
		{"empty encrypted reasoning", completed(map[string]any{"type": "reasoning", "id": "r", "summary": []any{}, "encrypted_content": "opaque"}), ""},
		{"rewritten optional summary", delta("r", 0, "Streamed summary.") + completed(summary("r", "Rewritten summary.")), "Streamed summary."},
	} {
		t.Run(check.name, func(t *testing.T) {
			var received strings.Builder
			_, err := parseResponsesStreamWithReasoning(strings.NewReader(check.stream), nil, func(fragment string) error { _, err := received.WriteString(fragment); return err })
			if err != nil || received.String() != check.want {
				t.Fatalf("reasoning=%q want=%q err=%v", received.String(), check.want, err)
			}
		})
	}
	abort := errors.New("could not persist terminal reasoning")
	if _, err := parseResponsesStreamWithReasoning(strings.NewReader(completed(summary("r", "Visible"))), nil, func(string) error { return abort }); !errors.Is(err, abort) {
		t.Fatalf("terminal reasoning lost sink failure: %v", err)
	}
}

func TestOpenAIResponsesRoutesReasoningThroughModelTurn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Reasoning struct {
				Summary string `json:"summary"`
			} `json:"reasoning"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Reasoning.Summary != "auto" {
			t.Errorf("reasoning summary option: %+v, err=%v", request, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"item_id\":\"rs_1\",\"delta\":\"Check.\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"Answer.\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "openai-responses", ID: "mock", BaseURL: server.URL, APIKey: "key", ReasoningSummary: "auto"}
	var reasoning strings.Builder
	turn, err := ModelTurnStreamWithReasoning(context.Background(), model, []ChatMessage{{Role: "user", Content: "solve"}}, nil, nil, func(fragment string) error { _, err := reasoning.WriteString(fragment); return err })
	if err != nil || turn.Text != "Answer." || reasoning.String() != "Check." {
		t.Fatalf("Responses dispatch: turn=%+v, reasoning=%q, err=%v", turn, reasoning.String(), err)
	}
}

func TestOpenAIResponsesReplaysEncryptedReasoningWithToolResult(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body struct {
			Store   *bool            `json:"store"`
			Include []string         `json:"include"`
			Input   []map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Store == nil || *body.Store {
			t.Error("stateless Responses request must disable server storage")
		}
		if len(body.Include) != 1 || body.Include[0] != "reasoning.encrypted_content" {
			t.Errorf("stateless Responses request must ask for encrypted reasoning: %v", body.Include)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			fmt.Fprint(w, `data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","delta":"Check."}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{}"},{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Working."}]},{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Check."}],"encrypted_content":"opaque-state"}]}}`+"\n\n")
			return
		}
		if len(body.Input) != 5 || body.Input[1]["type"] != "function_call" || body.Input[2]["type"] != "message" || body.Input[3]["type"] != "reasoning" || body.Input[3]["encrypted_content"] != "opaque-state" || body.Input[4]["type"] != "function_call_output" {
			t.Errorf("encrypted reasoning continuation missing: %+v", body.Input)
		}
		fmt.Fprint(w, `data: {"type":"response.output_text.delta","item_id":"msg_2","delta":"Done."}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[]}}`+"\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "openai-responses", ProviderID: "openai", ID: "mock", BaseURL: server.URL, APIKey: "key"}
	messages := []ChatMessage{{Role: "user", Content: "read"}}
	var reasoning strings.Builder
	first, err := ModelTurnStreamWithReasoning(context.Background(), model, messages, nil, nil, func(s string) error { _, err := reasoning.WriteString(s); return err })
	if err != nil || first.Text != "Working." || reasoning.String() != "Check." || len(first.Calls) != 1 || len(first.ResponseReasoning) != 1 {
		t.Fatalf("Responses reasoning tool turn: %+v reasoning=%q err=%v", first, reasoning.String(), err)
	}
	messages = append(messages, ChatMessage{Role: "assistant", ToolCalls: first.Calls, ResponseReasoning: first.ResponseReasoning, ResponseOutput: first.ResponseOutput}, ChatMessage{Role: "tool", ToolCallID: first.Calls[0].ID, Content: "file data"})
	second, err := ModelTurn(context.Background(), model, messages, nil)
	if err != nil || second.Text != "Done." || requests != 2 {
		t.Fatalf("Responses encrypted continuation: %+v requests=%d err=%v", second, requests, err)
	}
}

func TestOpenAIResponsesRequestsEncryptedReasoningOnInheritedRoutes(t *testing.T) {
	for _, providerID := range []string{"", "azure", "azure-cognitive-services"} {
		t.Run(providerID, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Include []string `json:"include"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if len(body.Include) != 1 || body.Include[0] != "reasoning.encrypted_content" {
					t.Errorf("missing encrypted reasoning request: %v", body.Include)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[]}}`+"\n\n")
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: "openai-responses", ProviderID: providerID, ID: "mock", BaseURL: server.URL, APIKey: "key", AzureNative: providerID != ""}
			if _, err := OpenAIResponsesTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOpenAIResponsesReasoningDefaultsAndOverrides(t *testing.T) {
	for _, check := range []struct {
		name, providerID, id, effort, summary, wantEffort, wantSummary string
	}{
		{"OpenAI GPT-5", "openai", "gpt-5", "", "", "", "auto"},
		{"OpenAI GPT-5.1", "openai", "gpt-5.1", "", "", "", "auto"},
		{"OpenAI GPT-5.4", "openai", "gpt-5.4", "", "", "", "auto"},
		{"Azure GPT-5", "azure", "gpt-5.4", "", "", "", "auto"},
		{"Codex unlisted GPT-5", "openai-codex", "gpt-5.4", "", "", "", ""},
		{"custom GPT-5", "custom", "gpt-5.4", "", "", "", "auto"},
		{"GPT-5 Chat", "openai", "gpt-5-chat-latest", "", "", "", ""},
		{"GPT-5 Pro", "openai", "gpt-5-pro", "", "", "", "auto"},
		{"explicit overrides", "openai", "gpt-5.4", "high", "concise", "high", "concise"},
		{"OpenAI Sol", "openai", "gpt-6.1-sol", "max", "", "max", "auto"},
		{"Codex Sol", "openai-codex", "gpt-6.1-sol", "max", "", "max", "auto"},
		{"Codex Sol default effort", "openai-codex", "gpt-6.1-sol", "", "", "", "auto"},
		{"custom Sol", "custom", "gpt-6.1-sol", "max", "", "max", "auto"},
		{"OpenCode Sol", "opencode", "gpt-6.1-sol", "max", "", "max", "auto"},
		{"OpenCode default effort", "opencode", "gpt-6.1-sol", "", "", "", "auto"},
		{"OpenCode Go reasoning", "opencode-go", "gpt-5.6-luna", "high", "", "high", "auto"},
		{"unlisted service explicit reasoning", "private-gateway", "private-model", "high", "", "high", "auto"},
		{"Sol default effort", "custom", "gpt-6.1-sol", "", "", "", "auto"},
		{"unlisted reasoning model", "custom", "private-reasoning-model", "high", "", "high", "auto"},
		{"unlisted default model", "custom", "private-model", "", "", "", ""},
		{"unlisted GPT-5 name", "custom", "private-gpt-5-compatible", "", "", "", ""},
		{"nonreasoning model", "openai", "gpt-4.1-mini", "", "", "", ""},
		{"disabled effort", "custom", "gpt-6.1-sol", "none", "", "none", ""},
		{"Sol summary override", "custom", "gpt-6.1-sol", "max", "detailed", "max", "detailed"},
	} {
		t.Run(check.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Reasoning map[string]string `json:"reasoning"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				_, hasEffort := body.Reasoning["effort"]
				if body.Reasoning["effort"] != check.wantEffort || hasEffort != (check.wantEffort != "") || body.Reasoning["summary"] != check.wantSummary {
					t.Errorf("reasoning = %v, want effort %q and summary %q", body.Reasoning, check.wantEffort, check.wantSummary)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[]}}`+"\n\n")
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: "openai-responses", ProviderID: check.providerID, ID: check.id, BaseURL: server.URL, APIKey: "key", AzureNative: check.providerID == "azure", ReasoningEffort: check.effort, ReasoningSummary: check.summary}
			if _, err := OpenAIResponsesTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMetaResponsesRequestsEncryptedReasoningForToolContinuation(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer meta-key" {
			t.Errorf("Meta route or credential: %s, %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body struct {
			Model     string           `json:"model"`
			Store     *bool            `json:"store"`
			Include   []string         `json:"include"`
			Input     []map[string]any `json:"input"`
			Reasoning map[string]any   `json:"reasoning"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "muse-spark-1.1" || body.Store == nil || *body.Store || len(body.Include) != 1 || body.Include[0] != "reasoning.encrypted_content" || body.Reasoning["effort"] != "high" {
			t.Errorf("Meta stateless request controls: %+v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"meta-state"},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{}"}]}}`+"\n\n")
			return
		}
		if len(body.Input) != 4 || body.Input[1]["type"] != "reasoning" || body.Input[1]["encrypted_content"] != "meta-state" || body.Input[2]["type"] != "function_call" || body.Input[3]["type"] != "function_call_output" {
			t.Errorf("Meta encrypted tool continuation: %+v", body.Input)
		}
		fmt.Fprint(w, `data: {"type":"response.output_text.delta","item_id":"msg_2","delta":"Done."}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[]}}`+"\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "openai-responses", ProviderID: "meta", ID: "muse-spark-1.1", BaseURL: server.URL + "/v1", APIKey: "meta-key", ReasoningEffort: "high"}
	messages := []ChatMessage{{Role: "user", Content: "read"}}
	first, err := ModelTurn(context.Background(), model, messages, nil)
	if err != nil || len(first.Calls) != 1 || len(first.ResponseReasoning) != 1 {
		t.Fatalf("Meta first turn: %+v, err=%v", first, err)
	}
	messages = append(messages, ChatMessage{Role: "assistant", ToolCalls: first.Calls, ResponseOutput: first.ResponseOutput}, ChatMessage{Role: "tool", ToolCallID: first.Calls[0].ID, Content: "file data"})
	second, err := ModelTurn(context.Background(), model, messages, nil)
	if err != nil || second.Text != "Done." || requests != 2 {
		t.Fatalf("Meta tool continuation: %+v, requests=%d, err=%v", second, requests, err)
	}
}

func TestXAIResponsesRequestsEncryptedReasoningForToolContinuation(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer xai-key" {
			t.Errorf("xAI route or credential: %s, %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body struct {
			Store     *bool            `json:"store"`
			Include   []string         `json:"include"`
			Reasoning map[string]any   `json:"reasoning"`
			Input     []map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Store == nil || *body.Store || len(body.Include) != 1 || body.Include[0] != "reasoning.encrypted_content" || body.Reasoning["effort"] != "high" {
			t.Errorf("xAI stateless reasoning controls: %+v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"xai-state"},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{}"}]}}`+"\n\n")
			return
		}
		if len(body.Input) != 4 || body.Input[1]["type"] != "reasoning" || body.Input[1]["encrypted_content"] != "xai-state" || body.Input[2]["type"] != "function_call" || body.Input[3]["type"] != "function_call_output" {
			t.Errorf("xAI encrypted tool continuation: %+v", body.Input)
		}
		fmt.Fprint(w, `data: {"type":"response.output_text.delta","item_id":"msg_2","delta":"Done."}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[]}}`+"\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "openai-responses", ProviderID: "xai", ID: "grok-4.3", BaseURL: server.URL + "/v1", APIKey: "xai-key", ReasoningEffort: "high"}
	messages := []ChatMessage{{Role: "user", Content: "read"}}
	first, err := ModelTurn(context.Background(), model, messages, nil)
	if err != nil || len(first.Calls) != 1 || len(first.ResponseReasoning) != 1 {
		t.Fatalf("xAI first turn: %+v, err=%v", first, err)
	}
	messages = append(messages, ChatMessage{Role: "assistant", ToolCalls: first.Calls, ResponseOutput: first.ResponseOutput}, ChatMessage{Role: "tool", ToolCallID: first.Calls[0].ID, Content: "file data"})
	second, err := ModelTurn(context.Background(), model, messages, nil)
	if err != nil || second.Text != "Done." || requests != 2 {
		t.Fatalf("xAI tool continuation: %+v, requests=%d, err=%v", second, requests, err)
	}
}

func TestOpenAIResponsesBoundsReasoningReplayItems(t *testing.T) {
	tooLarge := responseItem{Type: "reasoning", ID: "rs_1", EncryptedContent: strings.Repeat("x", 65<<10)}
	if _, err := responseReasoningItems([]responseItem{tooLarge}); err == nil || !strings.Contains(err.Error(), "limits") {
		t.Fatalf("oversized encrypted state accepted: %v", err)
	}
	malformed := responseItem{Type: "reasoning", ID: "rs_1"}
	malformed.Summary = append(malformed.Summary, struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{Type: "unknown", Text: "x"})
	if _, err := responseReasoningItems([]responseItem{malformed}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported reasoning summary accepted: %v", err)
	}
}

func TestOpenAIResponsesRejectsUnsupportedCompletedOutputItem(t *testing.T) {
	stream := `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"image_generation_call","id":"image_1"}]}}` + "\n\n"
	_, err := parseResponses(strings.NewReader(stream))
	if err == nil || !strings.Contains(err.Error(), "unsupported types") {
		t.Fatalf("unsupported completed output item was accepted: %v", err)
	}
}

func TestOpenAIResponsesRejectsPartialCompletedToolOutput(t *testing.T) {
	stream := `data: {"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read_file","arguments":"{}"}}` + "\n\n" +
		`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[]}]}}` + "\n\n"
	_, err := parseResponses(strings.NewReader(stream))
	if err == nil || !strings.Contains(err.Error(), "omits a function call") {
		t.Fatalf("partial completed output could lose its tool call: %v", err)
	}
}

func TestOpenAIResponsesBoundsCumulativeReasoning(t *testing.T) {
	delta := strings.Repeat("r", 90<<10)
	var stream strings.Builder
	for range 3 {
		item, err := json.Marshal(map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": "rs_1", "delta": delta})
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&stream, "data: %s\n\n", item)
	}
	stream.WriteString(`data: {"type":"response.completed","response":{"status":"completed","output":[]}}` + "\n\n")
	if _, err := parseResponsesStreamWithReasoning(strings.NewReader(stream.String()), nil, func(string) error { return nil }); err == nil || !strings.Contains(err.Error(), "reasoning delta exceeds") {
		t.Fatalf("oversized Responses reasoning accepted: %v", err)
	}
}

func TestOpenAIResponsesLongStreamedAnswerWithCompletedEcho(t *testing.T) {
	for _, test := range []struct {
		name, unit string
		chunk      int
	}{
		{name: "plain", unit: "x", chunk: 32 << 10},
		{name: "JSON escaped", unit: "<", chunk: 16 << 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			answer := strings.Repeat(test.unit, 200<<10)
			var stream strings.Builder
			for offset := 0; offset < len(answer); offset += test.chunk {
				end := min(offset+test.chunk, len(answer))
				event, err := json.Marshal(map[string]any{"type": "response.output_text.delta", "item_id": "msg_1", "delta": answer[offset:end]})
				if err != nil {
					t.Fatal(err)
				}
				fmt.Fprintf(&stream, "data: %s\n\n", event)
			}
			part := map[string]string{"type": "output_text", "text": answer}
			item := map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "content": []any{part}}
			for _, snapshot := range []any{
				map[string]any{"type": "response.output_text.done", "item_id": "msg_1", "text": answer},
				map[string]any{"type": "response.content_part.done", "item_id": "msg_1", "part": part},
				map[string]any{"type": "response.output_item.done", "item": item},
			} {
				event, err := json.Marshal(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				fmt.Fprintf(&stream, "data: %s\n\n", event)
			}
			completed, err := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{item}}})
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&stream, "data: %s\n\n", completed)
			var streamed strings.Builder
			turn, err := parseResponsesStream(strings.NewReader(stream.String()), func(text string) error {
				streamed.WriteString(text)
				return nil
			})
			if err != nil || turn.Text != answer || streamed.String() != answer || len(turn.ResponseOutput) != 0 {
				t.Fatalf("long Responses answer failed: bytes=%d replayItems=%d err=%v", len(turn.Text), len(turn.ResponseOutput), err)
			}
		})
	}
}

func TestOpenAIResponsesStillRejectsOversizedOrdinaryEvent(t *testing.T) {
	event, err := json.Marshal(map[string]any{"type": "response.output_text.delta", "item_id": "msg_1", "delta": strings.Repeat("x", 130<<10)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = parseResponses(strings.NewReader("data: " + string(event) + "\n\n"))
	if err == nil || !strings.Contains(err.Error(), "event exceeds 128 KiB") {
		t.Fatalf("oversized ordinary Responses event was accepted: %v", err)
	}
}

func TestOpenAIResponsesRejectsConflictingCompletedText(t *testing.T) {
	stream := `data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"first"}` + "\n\n" +
		`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"different"}]}]}}` + "\n\n"
	_, err := parseResponses(strings.NewReader(stream))
	if err == nil || !strings.Contains(err.Error(), "disagrees with streamed text") {
		t.Fatalf("conflicting Responses text was accepted: %v", err)
	}
}

func TestOpenAIResponsesDoesNotRetainLargeReasoningWithoutToolCall(t *testing.T) {
	output := []any{
		map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{}, "encrypted_content": strings.Repeat("x", 80<<10)},
		map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": "Done."}}},
	}
	event, err := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": output}})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := parseResponses(strings.NewReader("data: " + string(event) + "\n\n"))
	if err != nil || turn.Text != "Done." || len(turn.ResponseOutput) != 0 || len(turn.ResponseReasoning) != 0 {
		t.Fatalf("no-tool Responses answer retained opaque state: text=%q output=%d reasoning=%d err=%v", turn.Text, len(turn.ResponseOutput), len(turn.ResponseReasoning), err)
	}
}

func TestOpenAIResponsesRejectsIncompleteAndOversizedStreams(t *testing.T) {
	for _, test := range []struct{ name, stream, reason string }{
		{"incomplete", "data: {\"type\":\"response.incomplete\",\"response\":{\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n", "incomplete"},
		{"missing completion", "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg\",\"delta\":\"partial\"}\n\n", "before completion"},
		{"large text", strings.Repeat("data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg\",\"delta\":\""+strings.Repeat("x", 80<<10)+"\"}\n\n", 4), "exceeds 256 KiB"},
		{"large done text", "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"id\":\"msg\",\"content\":[{\"type\":\"output_text\",\"text\":\"" + strings.Repeat("x", maxReplyBytes+1) + "\"}]}}\n\n", "exceeds 256 KiB"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, test.stream)
			}))
			defer server.Close()
			_, err := OpenAIResponsesTurn(context.Background(), modelcfg.Model{Provider: "openai-responses", ID: "test", BaseURL: server.URL, APIKey: "key"}, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if err == nil || !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("expected %q, got %v", test.reason, err)
			}
		})
	}
}

func TestOpenAIResponsesCancellationStopsStream(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := OpenAIResponsesTurn(ctx, modelcfg.Model{Provider: "openai-responses", ID: "test", BaseURL: server.URL, APIKey: "key"}, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("Responses request did not reach server")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled Responses stream succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Responses stream did not stop after cancellation")
	}
}
