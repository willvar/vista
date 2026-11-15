package modeltrace

import (
	"net/http"
	"strings"
	"testing"
)

func TestCodexRoutingStateIsRedactedFromHeadersAndBodies(t *testing.T) {
	capture := &Capture{}
	headers := http.Header{
		"X-Codex-Turn-State":    {"opaque-routing-state"},
		"X-Codex-Turn-Metadata": {"private-turn-metadata"},
		"X-Codex-Routing-Hint":  {"private-routing-hint"},
	}
	capture.collectHeaders(headers)
	capture.cleanHeaders(headers)
	for _, values := range headers {
		if len(values) != 1 || values[0] != "[redacted]" {
			t.Fatal("routing state leaked in diagnostic headers")
		}
	}
	text := capture.redact("opaque-routing-state private-turn-metadata private-routing-hint", false)
	if strings.Contains(text, "private-") || strings.Contains(text, "opaque-") {
		t.Fatal("routing state leaked through an echoed body")
	}
}
