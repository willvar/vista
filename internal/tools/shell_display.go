package tools

import (
	"regexp"
	"strings"
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
