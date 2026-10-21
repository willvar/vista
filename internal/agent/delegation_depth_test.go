package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"vista/internal/agentconfig"
	"vista/internal/provider"
	"vista/internal/workerproto"
)

func delegationConfigFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	dir := filepath.Join(root, "vista")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDelegationConfiguredDepthAndRunSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name         string
		initialDepth int
		nextDepth    int
	}{
		{name: "default", initialDepth: 1, nextDepth: 1},
		{name: "disabled", initialDepth: 0, nextDepth: 0},
		{name: "depth3", initialDepth: 3, nextDepth: 3},
		{name: "maximum", initialDepth: agentconfig.MaxDelegationDepth, nextDepth: agentconfig.MaxDelegationDepth},
		{name: "increase applies next run", initialDepth: 1, nextDepth: 3},
		{name: "disable applies next run", initialDepth: 1, nextDepth: 0},
		{name: "enable applies next run", initialDepth: 0, nextDepth: 1},
		{name: "decrease applies next run", initialDepth: 3, nextDepth: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := delegationConfigFixture(t)
			if tc.initialDepth != agentconfig.DefaultDelegationDepth {
				if err := agentconfig.WriteSettings(dir, agentconfig.Settings{DelegationDepth: tc.initialDepth}); err != nil {
					t.Fatal(err)
				}
			}
			var runNumber atomic.Int32
			var blocked, reached atomic.Int32
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Messages []struct{ Role, Content string }
					Tools    []struct{ Function struct{ Name string } }
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) < 2 {
					t.Errorf("invalid mock request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				depth, err := strconv.Atoi(strings.TrimPrefix(request.Messages[1].Content, "level "))
				if err != nil {
					t.Errorf("invalid depth prompt: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				limit := tc.initialDepth
				if runNumber.Load() == 2 {
					limit = tc.nextDepth
				}
				hasDelegate := false
				for _, tool := range request.Tools {
					hasDelegate = hasDelegate || tool.Function.Name == "delegate"
				}
				if hasDelegate != (depth < limit) {
					t.Errorf("delegate schema at depth %d limit %d: present=%v", depth, limit, hasDelegate)
				}
				if depth > limit {
					t.Errorf("created Agent beyond depth %d: %d", limit, depth)
					continuationAnswer(w, "unexpected descendant")
					return
				}
				if request.Messages[len(request.Messages)-1].Role == "user" {
					if depth == 0 && runNumber.Load() == 1 && tc.initialDepth != tc.nextDepth {
						if err := agentconfig.WriteSettings(dir, agentconfig.Settings{DelegationDepth: tc.nextDepth}); err != nil {
							t.Errorf("change settings during run: %v", err)
							w.WriteHeader(http.StatusInternalServerError)
							return
						}
					}
					if depth == limit {
						reached.Add(1)
					}
					// Even at the leaf, deliberately return an unoffered delegate
					// call to verify the execution guard, not just schema filtering.
					arguments, _ := json.Marshal(map[string]string{"task": fmt.Sprintf("level %d", depth+1)})
					continuationEvent(w, mcpToolEvent(fmt.Sprintf("child-%d", depth+1), "delegate", string(arguments)))
					return
				}
				result := request.Messages[len(request.Messages)-1].Content
				var started delegationResult
				if json.Unmarshal([]byte(result), &started) == nil && started.Status == "running" {
					arguments, _ := json.Marshal(map[string]any{"taskIds": []string{started.TaskID}})
					continuationEvent(w, mcpToolEvent("collect-child", "await_agent", string(arguments)))
					return
				}
				if depth == limit {
					if !strings.Contains(result, fmt.Sprintf("delegation depth limit reached (%d)", limit)) {
						t.Errorf("unoffered delegate was not blocked: %s", result)
					} else {
						blocked.Add(1)
					}
				} else if !strings.Contains(result, fmt.Sprintf("level %d complete", depth+1)) {
					t.Errorf("missing descendant result at depth %d: %s", depth, result)
				}
				continuationAnswer(w, fmt.Sprintf("level %d complete", depth))
			}))
			defer model.Close()
			for number := int32(1); number <= 2; number++ {
				runNumber.Store(number)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				answer, err := Run(ctx, continuationCommand(model.URL, "level 0"), func(workerproto.Result) error { return nil })
				cancel()
				if err != nil || answer != "level 0 complete" {
					t.Fatalf("run %d: answer=%q error=%v", number, answer, err)
				}
			}
			if reached.Load() != 2 || blocked.Load() != 2 {
				t.Fatalf("leaf depth and execution guard: reached=%d blocked=%d", reached.Load(), blocked.Load())
			}
		})
	}
}

func TestRunScopeDelegationLimitDefaultsAndExplicitZero(t *testing.T) {
	var call provider.ToolCall
	call.Function.Name, call.Function.Arguments = "delegate", `{"task":"must not start"}`
	for _, scope := range []*runScope{nil, {}} {
		if _, err := (&toolRuntime{scope: scope, depth: 1}).execute(context.Background(), call); err == nil || !strings.Contains(err.Error(), "delegation depth limit reached (1)") {
			t.Fatalf("uninitialized scope did not enforce default: %v", err)
		}
	}
	scope := &runScope{delegationDepth: 0, delegationDepthSet: true}
	if limit := scope.delegationLimit(); limit != 0 {
		t.Fatalf("explicit zero changed to default: %d", limit)
	}
	if _, err := (&toolRuntime{scope: scope, depth: 0}).execute(context.Background(), call); err == nil || !strings.Contains(err.Error(), "delegation depth limit reached (0)") {
		t.Fatalf("explicit zero did not block delegation: %v", err)
	}
}
