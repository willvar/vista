package provider

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// The explicit-evidence rules are adapted from Pi (MIT), overflow.ts at
// b9ab2a6238d2c91cf0c6ebf836dfd05883f9db51, and OMP (MIT), error/flags.ts at
// f1ac114d8d3438bee43010aee559bfe720ba3224. See docs/context-compaction.md.
// Deliberately omitted: bare 400/413, generic token/payload limits, disconnects,
// and usage compared with catalog windows. Those do not confirm token overflow.
var contextOverflowPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bprompt (?:is )?too long\b|prompt 超长|prompt exceeds max length`),
	regexp.MustCompile(`(?i)\binput is too long for requested model\b`),
	regexp.MustCompile(`(?i)\bexceeds (?:the )?(?:model'?s )?(?:maximum )?context (?:window|length|size)\b`),
	regexp.MustCompile(`(?i)\binput token count.*exceeds the maximum\b`),
	regexp.MustCompile(`(?i)\bmaximum prompt length is [\d,]+.*(?:request|input|prompt)`),
	regexp.MustCompile(`(?i)\breduce the length of the messages\b`),
	regexp.MustCompile(`(?i)\bmaximum context length is [\d,]+ tokens\b.*(?:requested|resulted|input|messages)`),
	regexp.MustCompile(`(?i)\bexceeds (?:the )?maximum allowed input length of [\d,]+ tokens?\b`),
	regexp.MustCompile(`(?i)\binput(?: \([\d,]+ tokens\))? is longer than the model'?s context length\b`),
	regexp.MustCompile(`(?i)\bexceeds the available context size\b|greater than the context length\b`),
	regexp.MustCompile(`(?i)\bcontext window exceeds limit\b|exceeded model token limit\b`),
	regexp.MustCompile(`(?i)\btoo large for model with [\d,]+ maximum context length\b`),
	regexp.MustCompile(`(?i)\bprompt has [\d,]+ tokens?, but the configured context size is [\d,]+ tokens?\b`),
	regexp.MustCompile(`(?i)\brange of input length should be\s*\[\s*1\s*,\s*[\d,]+\s*\]`),
	regexp.MustCompile(`(?i)\brequested tokens?.*exceed.*context (?:window|length|size)\b`),
	regexp.MustCompile(`(?i)\bcontext (?:window|length|size).*(?:exceeded|overflow|too small)\b`),
	regexp.MustCompile(`(?i)\bprompt\s*\(\s*\d+\s+tokens\s*\)\s*(?:\+\s*max\s+tokens\s*\(\s*\d+\s*\)\s+exceeds\s+the|leaves\s+no\s+room\s+to\s+answer\s+in\s+the)\s+context\s*\(\s*\d+\s*\)`),
	regexp.MustCompile(`(?i)\b(?:context_length_exceeded|model_context_window_exceeded|ContextWindowExceededError)\b`),
	regexp.MustCompile(`(?i)\bnumber of input tokens exceeds maximum length\b`),
	regexp.MustCompile(`(?i)\bprompt token count of [\d,]+ exceeds the limit of [\d,]+\b`),
	regexp.MustCompile(`(?i)\b(?:prompt|input).*(?:too long|too large).*(?:context|n_ctx)\b`),
	regexp.MustCompile(`(?i)\brequested tokens?.*(?:exceeds?|greater than).*\bn_ctx\b`),
}

var nonContextOverflow = regexp.MustCompile(`(?i)rate.?limit|too many requests|throttl|tokens per (?:minute|second|day)|quota|insufficient.?credit|billing|invalid (?:api.?key|role)|authentication|unauthorized|max[_ ](?:completion[_ ]|output[_ ])?tokens (?:must|cannot|should)|(?:payload|image|body|request) (?:byte|size) limit`)

func IsContextOverflow(err error) bool {
	var failure *ModelHTTPError
	return errors.As(err, &failure) && failure.Code == "context_length_exceeded"
}

func overflowText(text string) bool {
	if nonContextOverflow.MatchString(text) {
		return false
	}
	for _, pattern := range contextOverflowPatterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}

// Only call on an error envelope, never an ordinary response or assistant text.
// New API/One API preserve message/code under error; OpenRouter wraps JSON in
// metadata.raw; some relays replace HTTP status or put the upstream code in a
// descriptive message. Classification must not depend on the HTTP status alone.
func errorEnvelopeOverflow(data []byte) bool {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return overflowText(string(data))
	}
	return overflowEvidence(value, 0)
}

func overflowEvidence(value any, depth int) bool {
	if depth > 8 {
		return false
	}
	switch v := value.(type) {
	case string:
		var nested any
		if json.Unmarshal([]byte(v), &nested) == nil {
			return overflowEvidence(nested, depth+1)
		}
		return overflowText(v)
	case map[string]any:
		for _, key := range []string{"code", "type", "status"} {
			code, _ := v[key].(string)
			if code == "context_length_exceeded" || code == "model_context_window_exceeded" {
				return true
			}
		}
		for _, key := range []string{"code", "type", "status", "message"} {
			text, _ := v[key].(string)
			if nonContextOverflow.MatchString(text) || text == "RESOURCE_EXHAUSTED" {
				return false
			}
		}
		if param, _ := v["param"].(string); strings.Contains(param, "role") || param == "max_tokens" || param == "max_output_tokens" || param == "max_completion_tokens" {
			return false
		}
		for _, key := range []string{"error", "response", "message", "detail", "errorMessage", "errorText", "errorArgs", "reason", "metadata", "raw", "cause", "innererror"} {
			if overflowEvidence(v[key], depth+1) {
				return true
			}
		}
	}
	return false
}

func modelStreamError(name string, data []byte) error {
	code := modelErrorCode(data)
	if errorEnvelopeOverflow(data) {
		code = "context_length_exceeded"
	}
	return &ModelHTTPError{Provider: name, Code: code, Message: modelErrorMessage(data)}
}

func contextStopError(name, reason string) error {
	if reason == "model_context_window_exceeded" || reason == "context_length_exceeded" {
		return &ModelHTTPError{Provider: name, Code: "context_length_exceeded"}
	}
	return nil
}
