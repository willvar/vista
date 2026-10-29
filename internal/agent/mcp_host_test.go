package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vista/internal/mcphost"
	modelcfg "vista/internal/model"
	"vista/internal/workerproto"
)

func TestMCPHostUnavailableDoesNotBlockModelReply(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"finished\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = mcphost.WithEndpointResolver(ctx, func(ctx context.Context) (workerproto.MCPHost, error) {
		<-ctx.Done()
		return workerproto.MCPHost{}, ctx.Err()
	})
	done := make(chan error, 1)
	go func() {
		answer, err := Run(ctx, workerproto.Command{RunID: "unrelated", Prompt: "Reply without tools", MCPHost: workerproto.MCPHost{Managed: true}, Model: modelcfg.Model{Provider: "openai-chat", ID: "fixture", APIKey: "test", BaseURL: upstream.URL}}, func(workerproto.Result) error { return nil })
		if err == nil && answer != "finished" {
			err = fmt.Errorf("unexpected answer %q", answer)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("MCP host readiness blocked an unrelated model reply")
	}
}
