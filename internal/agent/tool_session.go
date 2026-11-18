package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"vista/internal/instance"
	"vista/internal/provider"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func openSessionStore() (*store.Store, error) {
	dir, err := instance.Dir()
	if err != nil {
		return nil, err
	}
	return store.OpenReadOnly(filepath.Join(dir, "vista.db"))
}

var listSessionsSchema = map[string]any{"type": "function", "function": map[string]any{
	"name":        "list_sessions",
	"description": "Find saved conversations in this Vista instance by title, open state or last activity. Returns metadata and work flags, never message previews or bodies. Use idleOnly for cleanup suggestions: excludes the current, pinned, unread and non-idle sessions. Use nextOffset to continue. Use read_session for messages.",
	"parameters": map[string]any{"type": "object", "properties": map[string]any{
		"title":         map[string]any{"type": "string", "description": "Case-insensitive substring of title."},
		"open":          map[string]any{"type": "boolean", "description": "Omit for both open and closed conversations."},
		"updatedBefore": map[string]any{"type": "string", "description": "Exclusive RFC 3339 timestamp, including time zone."},
		"idleOnly":      map[string]any{"type": "boolean"},
		"offset":        map[string]any{"type": "integer", "minimum": 0},
		"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": 20},
	}},
}}

func (r *toolRuntime) listSessions(ctx context.Context, call provider.ToolCall) (string, error) {
	var input struct {
		Title         string `json:"title"`
		Open          *bool  `json:"open"`
		UpdatedBefore string `json:"updatedBefore"`
		IdleOnly      bool   `json:"idleOnly"`
		Offset        int    `json:"offset"`
		Limit         int    `json:"limit"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	if input.Offset < 0 || input.Limit < 0 || input.Limit > 20 {
		return "", errors.New("invalid session page range")
	}
	if input.Limit == 0 {
		input.Limit = 10
	}
	var before time.Time
	if input.UpdatedBefore != "" {
		var err error
		before, err = time.Parse(time.RFC3339, input.UpdatedBefore)
		if err != nil || before.IsZero() {
			return "", errors.New("updatedBefore must be an RFC 3339 timestamp")
		}
	}
	db, err := openSessionStore()
	if err != nil {
		return "", err
	}
	defer db.Close()
	items, err := db.AgentSessions(ctx, r.parent.SessionID, strings.TrimSpace(input.Title), input.Open, before, input.IdleOnly, input.Offset, input.Limit+1)
	if err != nil {
		return "", err
	}
	for n := min(input.Limit, len(items)); ; n-- {
		value := map[string]any{"sessions": items[:n], "currentSessionId": r.parent.SessionID, "hasMore": n < len(items)}
		if n < len(items) {
			value["nextOffset"] = input.Offset + n
		}
		output, err := marshalResult(value)
		if err != nil || !exceedsToolBudget(output) {
			return output, err
		}
		if n <= 1 {
			return "", errors.New("session title exceeds the tool result budget")
		}
	}
}

func sessionActionSchema(name, description string) map[string]any {
	return map[string]any{"type": "function", "function": map[string]any{
		"name": name, "description": description + " Vista displays the exact eligible list for user confirmation; a message or a tool argument cannot approve it. Main Agent only. Call alone and wait; do not retry automatically after refusal or a skipped result.",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"sessionIds": map[string]any{"type": "array", "minItems": 1, "maxItems": workerproto.MaxSessionActionTargets, "items": map[string]any{"type": "string"}},
		}, "required": []string{"sessionIds"}},
	}}
}

var closeSessionsSchema = sessionActionSchema("close_sessions", "Request closing explicit IDs of other idle open conversations. Closing keeps history; it is the default for tidying idle sessions. No automatic replacement of skipped targets.")
var deleteSessionsSchema = sessionActionSchema("delete_sessions", "Only when the user explicitly requests deletion, request permanent deletion of explicit IDs of already closed conversations. Do not close a conversation to make it eligible. Deletion is irreversible in Vista.")

type sessionActionRequesterKey struct{}
type sessionActionRequester func(context.Context, workerproto.Result) (*workerproto.SessionActionResult, error)

func WithSessionActionRequester(ctx context.Context, request sessionActionRequester) context.Context {
	return context.WithValue(ctx, sessionActionRequesterKey{}, request)
}

func isSessionAction(name string) bool { return name == "close_sessions" || name == "delete_sessions" }

func (r *toolRuntime) requestSessionAction(ctx context.Context, call provider.ToolCall) (string, error) {
	if r.depth != 0 || r.parent.ParentTaskID != "" || r.parent.TaskID != "" && r.parent.TaskID != "main" || r.parent.RunID == "" {
		return "", errors.New("only the active main Agent can request a session action")
	}
	var input struct {
		SessionIDs []string `json:"sessionIds"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	action := workerproto.SessionAction{Kind: strings.TrimSuffix(call.Function.Name, "_sessions"), SessionIDs: input.SessionIDs}
	if err := action.Validate(r.parent.SessionID); err != nil {
		return "", err
	}
	request, _ := ctx.Value(sessionActionRequesterKey{}).(sessionActionRequester)
	if request == nil {
		return "", errors.New("session action confirmation is unavailable")
	}
	result, err := request(ctx, workerproto.Result{Kind: "session_action", RunID: r.parent.RunID, SessionAction: &action})
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", errors.New("missing session action result")
	}
	return marshalResult(result)
}
