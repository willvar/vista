package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/net/http/httpguts"

	"vista/internal/appdirs"
	"vista/internal/buildinfo"
	"vista/internal/useragent"
	"vista/internal/workerproto"
)

const ProtocolVersion = "2025-11-25"
const maxHTTPResponse = 256 << 10
const maxToolsPerPage = 64

type Config = workerproto.MCPServer
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}
type ToolPage struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
}
type CallResult struct {
	Content    string `json:"content"`
	Structured any    `json:"structured,omitempty"`
	IsError    bool   `json:"isError"`
}
type Server struct {
	session     *mcp.ClientSession
	command     *exec.Cmd
	outputLimit int
}

func Open(ctx context.Context, config Config) (*Server, error) {
	var dir string
	if config.OAuth {
		var err error
		dir, err = appdirs.Config()
		if err != nil {
			return nil, err
		}
	}
	return OpenWithConfigDir(ctx, config, dir)
}

// OpenWithConfigDir uses the same credential directory as the caller's settings.
func OpenWithConfigDir(ctx context.Context, config Config, dir string) (*Server, error) {
	if err := ValidateConfig(config); err != nil {
		return nil, err
	}
	var handler auth.OAuthHandler
	if config.OAuth {
		path := oauthPath(dir, config.ID)
		credential, err := readOAuth(path, config.Endpoint)
		if err != nil {
			return nil, fmt.Errorf("MCP %s needs OAuth login; authorize it in WebUI Settings > MCP: %w", config.Name, err)
		}
		if config.ClientID != "" && credential.Config.ClientID != config.ClientID {
			return nil, fmt.Errorf("MCP %s OAuth client changed; reauthorize it in WebUI Settings > MCP", config.Name)
		}
		handler = &storedOAuthHandler{name: config.Name, source: &storedTokenSource{path: path, endpoint: config.Endpoint}}
	}
	return openWithHandler(ctx, config, handler, 10*time.Second, maxHTTPResponse)
}

// OpenSearch leaves result sizing to the user's websearch character setting.
// Ordinary MCP connections retain their separate transport bound.
func OpenSearch(ctx context.Context, endpoint, token string) (*Server, error) {
	config := Config{ID: "search", Name: "search", Transport: "http", Endpoint: endpoint, Token: token}
	if err := ValidateConfig(config); err != nil {
		return nil, err
	}
	return openWithHandler(ctx, config, nil, 10*time.Second, -1)
}

func openWithHandler(ctx context.Context, config Config, handler auth.OAuthHandler, connectTimeout time.Duration, outputLimit int) (*Server, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "vista", Version: buildinfo.Version}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
	var transport mcp.Transport
	var command *exec.Cmd
	var local *commandTransport
	switch config.Transport {
	case "stdio":
		command = exec.Command(config.Command, config.Args...)
		command.Dir = config.Directory
		command.Stderr = io.Discard
		local = &commandTransport{command: command}
		transport = local
	case "http":
		base := http.DefaultTransport.(*http.Transport).Clone()
		base.DisableKeepAlives = true
		httpClient := &http.Client{Transport: &boundedTransport{base: base, token: config.Token, headers: config.Headers, limit: outputLimit}, Timeout: max(30*time.Second, connectTimeout), CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("MCP endpoint redirected") }}
		transport = &mcp.StreamableClientTransport{Endpoint: config.Endpoint, HTTPClient: httpClient, MaxRetries: -1, MaxEventSize: outputLimit, DisableStandaloneSSE: true, OAuthHandler: handler}
	}
	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	session, err := client.Connect(connectCtx, transport, &mcp.ClientSessionOptions{ProtocolVersion: ProtocolVersion})
	if err != nil {
		if local != nil && local.conn != nil {
			_ = local.conn.Close()
		}
		return nil, fmt.Errorf("connect MCP %s: %w", config.Name, err)
	}
	if initialized := session.InitializeResult(); initialized == nil || initialized.ProtocolVersion != ProtocolVersion {
		_ = session.Close()
		return nil, fmt.Errorf("MCP %s did not negotiate protocol %s", config.Name, ProtocolVersion)
	}
	return &Server{session: session, command: command, outputLimit: outputLimit}, nil
}

func ValidateConfig(config Config) error {
	if config.ID == "" || len(config.ID) > 64 {
		return errors.New("MCP server ID must contain 1–64 characters")
	}
	for _, ch := range config.ID {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
			return errors.New("MCP server ID permits only letters, digits, underscores and hyphens")
		}
	}
	if config.Name == "" || strings.TrimSpace(config.Name) != config.Name || utf8.RuneCountInString(config.Name) > 64 || strings.ContainsFunc(config.Name, unicode.IsControl) {
		return errors.New("MCP server name must contain 1–64 characters without control characters or surrounding whitespace")
	}
	if len(config.Token) > 4096 || len(config.ClientID) > 1024 || len(config.ClientSecret) > 4096 {
		return errors.New("invalid MCP credential length")
	}
	if len(config.Headers) > 32 {
		return errors.New("configure at most 32 MCP request headers")
	}
	seenHeaders, headerBytes := map[string]bool{}, 0
	for name, value := range config.Headers {
		key := http.CanonicalHeaderKey(name)
		if !httpguts.ValidHeaderFieldName(name) || !httpguts.ValidHeaderFieldValue(value) || seenHeaders[key] {
			return errors.New("MCP request headers contain an invalid or duplicate name or value")
		}
		switch key {
		case "Host", "Content-Length", "Transfer-Encoding", "Connection", "Accept", "Content-Type", "Mcp-Protocol-Version", "Mcp-Session-Id":
			return errors.New("MCP request headers cannot override HTTP framing or MCP protocol headers")
		case "Authorization":
			if config.OAuth || config.Token != "" {
				return errors.New("MCP Authorization header conflicts with token or OAuth authentication")
			}
		}
		seenHeaders[key] = true
		headerBytes += len(name) + len(value)
	}
	if headerBytes > 8192 {
		return errors.New("MCP request headers exceed 8 KiB")
	}
	if config.Transport == "stdio" {
		if config.OAuth || config.ClientID != "" || config.ClientSecret != "" || config.Token != "" || len(config.Headers) > 0 {
			return errors.New("MCP stdio servers do not accept HTTP credentials")
		}
		if !filepath.IsAbs(config.Command) || !filepath.IsAbs(config.Directory) || len(config.Args) > 32 {
			return errors.New("MCP stdio command and directory must be absolute and arguments limited")
		}
		for _, arg := range config.Args {
			if len(arg) > 1024 {
				return errors.New("MCP stdio argument exceeds 1 KiB")
			}
		}
		return nil
	}
	if config.Transport != "http" {
		return errors.New("MCP transport must be stdio or http")
	}
	if config.OAuth && config.Token != "" || !config.OAuth && (config.ClientID != "" || config.ClientSecret != "") {
		return errors.New("MCP OAuth configuration conflicts with static token or client credentials")
	}
	if config.OAuth && config.ClientID == "" && config.ClientSecret != "" {
		return errors.New("MCP OAuth client secret requires a client ID")
	}
	u, err := url.Parse(config.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && loopback(u.Hostname()))) {
		return errors.New("MCP endpoint requires HTTPS or loopback HTTP")
	}
	return nil
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) Close() error {
	if s == nil || s.session == nil {
		return nil
	}
	return s.session.Close()
}

func (s *Server) ListTools(ctx context.Context, cursor string) (ToolPage, error) {
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := s.session.ListTools(callCtx, &mcp.ListToolsParams{Cursor: cursor})
	if err != nil {
		return ToolPage{}, err
	}
	if result == nil || len(result.NextCursor) > 512 {
		return ToolPage{}, errors.New("MCP tool page or cursor is invalid")
	}
	if len(result.Tools) > maxToolsPerPage {
		return ToolPage{}, errors.New("MCP tool page exceeds 64 tools")
	}
	page := ToolPage{Tools: make([]Tool, 0, len(result.Tools)), NextCursor: result.NextCursor}
	for _, tool := range result.Tools {
		if tool == nil || len(tool.Name) > 128 || len(tool.Description) > 2048 {
			return ToolPage{}, errors.New("MCP tool metadata exceeds limits")
		}
		encoded, err := json.Marshal(tool.InputSchema)
		if err != nil || len(encoded) > 8<<10 {
			return ToolPage{}, errors.New("MCP tool schema exceeds 8 KiB")
		}
		page.Tools = append(page.Tools, Tool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
	}
	return page, nil
}

func (s *Server) CallTool(ctx context.Context, name string, arguments json.RawMessage) (CallResult, error) {
	limit := s.outputLimit
	if name == "" || len(name) > 128 || len(arguments) > 4096 {
		return CallResult{}, errors.New("MCP tool name or arguments exceed limits")
	}
	var args map[string]any
	if err := json.Unmarshal(arguments, &args); err != nil || args == nil {
		return CallResult{}, errors.New("MCP tool arguments must be a JSON object")
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := s.session.CallTool(callCtx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return CallResult{}, err
	}
	if result == nil {
		return CallResult{}, errors.New("MCP tool returned no result")
	}
	if result.NeedsInput() {
		return CallResult{}, errors.New("MCP tool requires unsupported client input")
	}
	out := CallResult{IsError: result.IsError, Structured: result.StructuredContent}
	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		if !ok {
			return CallResult{}, errors.New("MCP tool returned unsupported non-text content")
		}
		if limit > 0 && len(out.Content)+len(text.Text)+1 > limit {
			return CallResult{}, fmt.Errorf("MCP tool result exceeds %d KiB", limit>>10)
		}
		if out.Content != "" {
			out.Content += "\n"
		}
		out.Content += text.Text
	}
	if limit > 0 {
		encoded, err := json.Marshal(out)
		if err != nil {
			return CallResult{}, err
		}
		if len(encoded) > limit {
			return CallResult{}, fmt.Errorf("MCP tool result exceeds %d KiB", limit>>10)
		}
	}
	return out, nil
}

type boundedTransport struct {
	base    http.RoundTripper
	token   string
	headers map[string]string
	limit   int
}

func (t *boundedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	r := request.Clone(request.Context())
	r.Header = request.Header.Clone()
	for name, value := range t.headers {
		r.Header.Set(name, value)
	}
	if t.token != "" {
		r.Header.Set("Authorization", "Bearer "+t.token)
	}
	response, err := (useragent.Transport{Base: t.base}).RoundTrip(r)
	if err != nil {
		return nil, err
	}
	if t.limit <= 0 {
		return response, nil
	}
	if response.ContentLength > int64(t.limit) {
		response.Body.Close()
		return nil, errors.New("MCP HTTP response exceeds 256 KiB")
	}
	response.Body = &boundedBody{ReadCloser: response.Body, remaining: int64(t.limit)}
	return response, nil
}

type boundedBody struct {
	io.ReadCloser
	remaining int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		var probe [1]byte
		n, err := b.ReadCloser.Read(probe[:])
		if n > 0 {
			return 0, errors.New("MCP HTTP response exceeds 256 KiB")
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}
