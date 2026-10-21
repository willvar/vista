// Package useragent owns Vista's default outbound HTTP identity. Settings are
// read per request so the server and resident Workers observe the same changes.
package useragent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"vista/internal/appdirs"
	"vista/internal/buildinfo"
	"vista/internal/platform"
)

const Filename = "user-agent.json"
const MaxLength = 512

// CopilotUserAgent is shared by the preset and native Copilot transport.
const CopilotUserAgent = "copilot/" + CopilotVersion

type Config struct {
	Preset string `json:"preset"`
	Custom string `json:"custom"`
}

type Preset struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Compatibility identities are bundled examples, not a claim that another
// client is installed or that its latest version has been discovered.
// Formats: openai/codex login/src/auth/default_client.rs; anomalyco/opencode;
// can1357/oh-my-pi packages/utils/src/dirs.ts; anthropics/claude-code.
// Gemini: google-gemini/gemini-cli packages/core/src/core/contentGenerator.ts.
// Kimi: MoonshotAI/kimi-code apps/kimi-code/src/constant/app.ts.
// Cline: cline/cline sdk/packages/llms/src/providers/cline-client-headers.ts.
func Presets() []Preset {
	return []Preset{
		{ID: "vista", Name: "Vista", Value: vista(buildinfo.Version, runtime.GOOS, runtime.GOARCH)},
		{ID: "codex", Name: "Codex", Value: Codex()},
		{ID: "opencode", Name: "OpenCode", Value: "opencode/" + OpenCodeVersion},
		{ID: "ohmypi", Name: "OhMyPi", Value: "omp/" + OhMyPiVersion},
		{ID: "claude-code", Name: "Claude Code", Value: "claude-cli/" + ClaudeCodeVersion + " (external, cli)"},
		// Keep Gemini's product/version prefix without model-specific metadata:
		// this preset also applies to requests without a model, such as webfetch.
		{ID: "gemini-cli", Name: "Gemini CLI", Value: "GeminiCLI/" + GeminiCLIVersion},
		{ID: "kimi-code", Name: "Kimi Code", Value: "kimi-code-cli/" + KimiCodeVersion},
		{ID: "cline", Name: "Cline", Value: "Cline/" + ClineVersion},
		{ID: "github-copilot", Name: "GitHub Copilot", Value: CopilotUserAgent},
	}
}

func vista(version, goos, goarch string) string {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if version == "" || strings.ContainsAny(version, " ()/;\\\t\r\n") {
		version = "dev"
	}
	return "vista/" + version + " " + platformComment(goos, goarch)
}

func platformComment(goos, goarch string) string {
	osName := map[string]string{"linux": "Linux", "darwin": "macOS", "windows": "Windows"}[goos]
	if osName == "" {
		osName = goos
	}
	if goarch == "amd64" {
		goarch = "x86_64"
	}
	return "(" + osName + "; " + goarch + ")"
}

func (c Config) Validate() error {
	if len(c.Custom) > MaxLength {
		return fmt.Errorf("custom User-Agent must be at most %d characters", MaxLength)
	}
	for _, ch := range []byte(c.Custom) {
		if ch < ' ' || ch > '~' {
			return errors.New("custom User-Agent must contain only printable ASCII characters on one line")
		}
	}
	if c.Preset == "custom" {
		if strings.TrimSpace(c.Custom) == "" {
			return errors.New("custom User-Agent must not be empty")
		}
		return nil
	}
	for _, preset := range Presets() {
		if preset.ID == c.Preset {
			return nil
		}
	}
	return errors.New("unknown User-Agent preset")
}

func (c Config) Value() string {
	if c.Preset == "custom" {
		return strings.TrimSpace(c.Custom)
	}
	for _, preset := range Presets() {
		if preset.ID == c.Preset {
			return preset.Value
		}
	}
	return ""
}

func Read(dir string) (Config, error) {
	file, err := os.Open(filepath.Join(dir, Filename))
	if errors.Is(err, os.ErrNotExist) {
		return Config{Preset: "vista"}, nil
	}
	if err != nil {
		return Config{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return Config{}, errors.New("could not read User-Agent settings within 4 KiB")
	}
	var c Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return Config{}, errors.New("invalid User-Agent settings")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("invalid User-Agent settings")
	}
	return c, c.Validate()
}

func Write(dir string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || !platform.PrivateMode(info.Mode()) {
		return errors.New("Vista configuration directory must be private")
	}
	c.Custom = strings.TrimSpace(c.Custom)
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".user-agent-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, Filename))
}

func Current() (string, error) {
	dir, err := appdirs.Config()
	if err != nil {
		return "", err
	}
	c, err := Read(dir)
	if err != nil {
		return "", fmt.Errorf("read User-Agent settings: %w", err)
	}
	return c.Value(), nil
}

// requestWithAgent preserves explicitly supplied headers, including an empty
// User-Agent that suppresses the header. It never mutates the caller's request.
func requestWithAgent(request *http.Request, value string) (*http.Request, error) {
	if _, exists := request.Header["User-Agent"]; exists {
		return request, nil
	}
	if value == "" {
		var err error
		value, err = Current()
		if err != nil {
			return nil, err
		}
	}
	request = request.Clone(request.Context())
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	request.Header.Set("User-Agent", value)
	return request, nil
}

func Do(client *http.Client, request *http.Request) (*http.Response, error) {
	request, err := requestWithAgent(request, "")
	if err != nil {
		return nil, err
	}
	return client.Do(request)
}

// Transport supplies a default without replacing explicit SDK headers.
// A nonempty UserAgent pins a channel's identity without reading user settings.
type Transport struct {
	Base      http.RoundTripper
	UserAgent string
}

func (t Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	request, err := requestWithAgent(request, t.UserAgent)
	if err != nil {
		return nil, err
	}
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(request)
}
