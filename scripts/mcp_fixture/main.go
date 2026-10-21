package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func fixture() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "vista-e2e-fixture", Version: "1"}, &mcp.ServerOptions{PageSize: 1})
	server.AddTool(&mcp.Tool{Name: "echo", Description: "Echo a value", InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","maxLength":4096},"repeat":{"type":"integer","minimum":1,"maximum":64}},"required":["text"]}`)}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input struct {
			Text   string `json:"text"`
			Repeat int    `json:"repeat"`
		}
		if err := json.Unmarshal(request.Params.Arguments, &input); err != nil {
			return nil, err
		}
		if input.Repeat == 0 {
			input.Repeat = 1
		}
		if input.Repeat < 1 || input.Repeat > 64 || len(input.Text) > 4096 {
			return nil, fmt.Errorf("echo fixture text or repeat exceeds limits")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fixture:" + strings.Repeat(input.Text, input.Repeat)}}}, nil
	})
	server.AddTool(&mcp.Tool{Name: "z_extra", Description: "Second page marker", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "extra"}}}, nil
	})
	return server
}

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: mcp-fixture stdio|http")
	}
	switch os.Args[1] {
	case "stdio":
		if err := fixture().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			log.Fatal(err)
		}
	case "http":
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			log.Fatal(err)
		}
		handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fixture() }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true})
		fmt.Println(listener.Addr().String())
		if err := http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer fixture-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			handler.ServeHTTP(w, r)
		})); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatal("usage: mcp-fixture stdio|http")
	}
}
