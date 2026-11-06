package workerproto

import (
	"strings"
	"unicode/utf8"
)

// TextPreview bounds display metadata, including its truncation marker. Keep
// full execution inputs and durable output outside this projection.
func TextPreview(text string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	text = strings.ToValidUTF8(text, "�")
	if len(text) <= maximum {
		return text
	}
	suffix := "…"
	if maximum < len(suffix) {
		suffix = ""
	}
	end := maximum - len(suffix)
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + suffix
}
