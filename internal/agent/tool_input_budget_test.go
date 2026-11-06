package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"vista/internal/toolinput"
	"vista/internal/workerproto"
)

func TestToolInputBudgetRejectsBeforeDecodingAndExplainsRecovery(t *testing.T) {
	arguments := `{"command":"` + strings.Repeat("x", toolinput.MaxArgumentsBytes) + `"}`
	var input struct{ Command string }
	err := decodeArguments(arguments, &input)
	var inputErr *toolinput.Error
	if !errors.As(err, &inputErr) || input.Command != "" {
		t.Fatalf("oversized arguments reached execution: %v", err)
	}
	var result struct {
		Executed              *bool
		ActualBytes, MaxBytes int
		Hint                  string
	}
	if json.Unmarshal([]byte(mergeToolError("", err)), &result) != nil || result.Executed == nil || *result.Executed || result.ActualBytes != len(arguments) || result.MaxBytes != toolinput.MaxArgumentsBytes || result.Hint == "" {
		t.Fatalf("missing recovery metadata: %+v", result)
	}
	if err := decodeArguments(`{"command":"`+strings.Repeat("x", toolinput.MaxArgumentsBytes-14)+`"}`, &input); err != nil {
		t.Fatalf("valid JSON at the complete argument boundary rejected: %v", err)
	}
}

func TestDelegateOverBudgetDoesNotStartChild(t *testing.T) {
	for _, task := range []string{strings.Repeat("中", workerproto.MaxPromptBytes/3+1), " \n "} {
		scope := &runScope{delegations: newDelegationRegistry()}
		started := false
		r := &toolRuntime{scope: scope, parent: workerproto.Command{TaskID: "main"}, emit: func(workerproto.Result) error { started = true; return nil }}
		arguments, _ := json.Marshal(map[string]string{"task": task})
		_, err := r.delegate(context.Background(), scriptedToolCall("child", "delegate", string(arguments)))
		var inputErr *toolinput.Error
		if !errors.As(err, &inputErr) || inputErr.ActualBytes != len(task) || inputErr.MaxBytes != workerproto.MaxPromptBytes || started {
			t.Fatalf("invalid delegation started work: %v, started=%v", err, started)
		}
	}
	declaration, _ := json.Marshal(delegateSchema)
	command, _ := json.Marshal(shellSchema)
	if !strings.Contains(string(declaration), fmt.Sprint(workerproto.MaxPromptBytes)) || !strings.Contains(string(command), fmt.Sprint(toolinput.MaxArgumentsBytes)) {
		t.Fatal("model tool schemas disagree with validation limits")
	}
}
