package tools

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"vista/internal/workerproto"
)

var shellSecretPatterns = []*regexp.Regexp{
	// Environment variables, headers, JSON fields, and URL query parameters.
	regexp.MustCompile(`(?i)(\b[a-z0-9_-]*(?:api[_-]?key|token|password|passwd|secret)[a-z0-9_-]*["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s'";&|,})]+)`),
	// Command-line options, including a quoted value with spaces.
	regexp.MustCompile(`(?i)((?:--?)[a-z0-9_-]*(?:api[_-]?key|token|password|passwd|secret)[a-z0-9_-]*[\t ]+)(?:"[^"]*"|'[^']*'|[^\s'";&|,})]+)`),
	regexp.MustCompile(`(?i)(\bauthorization["']?\s*[:=]\s*["']?(?:(?:bearer|basic)[\t ]+)?)[^\s'";&|,})]+`),
	regexp.MustCompile(`(?i)((?:--user|-u)[\t ]+)(?:"[^"]*"|'[^']*'|[^\s'";&|]+)`),
}

var shellURLCredentials = regexp.MustCompile(`(?i)(https?://)[^\s/@]+:[^\s/@]+(@)`)

// DisplayShellCommand projects execution input into metadata safe to record or
// display. Never pass this projection back to the shell or apply it to stdout.
func DisplayShellCommand(command string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			command = strings.ReplaceAll(command, secret, "[redacted]")
		}
	}
	for _, pattern := range shellSecretPatterns {
		command = pattern.ReplaceAllString(command, "${1}[redacted]")
	}
	return shellURLCredentials.ReplaceAllString(command, "${1}[redacted]${2}")
}

// Display metadata has its own bound; the full command remains execution input.
// Even JSON's worst-case escaping leaves room for both fields in a Worker frame.
func ShellCommandPreview(command string, secrets []string) string {
	command = DisplayShellCommand(command, secrets)
	if len(command) <= workerproto.MaxCommandPreviewBytes {
		return command
	}
	const suffix = "\n[Command preview truncated.]"
	end := workerproto.MaxCommandPreviewBytes - len(suffix)
	for end > 0 && !utf8.ValidString(command[:end]) {
		end--
	}
	return command[:end] + suffix
}
