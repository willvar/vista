package workerproto

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestErrorPreviewSurvivesJSONBoundaries(t *testing.T) {
	for _, limit := range []int{0, 1, 2, 3, MaxErrorBytes, MaxPromptBytes} {
		for _, text := range []string{"short", strings.Repeat("界🙂", MaxPromptBytes), strings.Repeat("\xff", MaxPromptBytes), strings.Repeat("\x00", MaxPromptBytes)} {
			preview := TextPreview(text, limit)
			encoded, err := json.Marshal(preview)
			var decoded string
			if err != nil || json.Unmarshal(encoded, &decoded) != nil || len(decoded) > limit || !utf8.ValidString(preview) || decoded != preview {
				t.Fatalf("preview changed or exceeded %d bytes across JSON: before=%d after=%d error=%v", limit, len(preview), len(decoded), err)
			}
			if len(text) <= limit && utf8.ValidString(text) && preview != text {
				t.Fatal("short error text was altered")
			}
		}
	}
}
