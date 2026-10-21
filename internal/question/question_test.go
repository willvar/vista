package question

import (
	"strings"
	"testing"
)

func TestAnswersValidateSelectionAndCustomModes(t *testing.T) {
	no := false
	base := Question{Question: "Which language?", Options: []Option{{Label: "Go"}, {Label: "TypeScript"}}}
	for _, tc := range []struct {
		name     string
		multiple bool
		custom   *bool
		answer   Answer
		valid    bool
	}{
		{"single", false, nil, Answer{Options: []string{"Go"}}, true},
		{"default custom", false, nil, Answer{Text: " Rust "}, true},
		{"multi", true, &no, Answer{Options: []string{"Go", "TypeScript"}}, true},
		{"multi plus custom", true, nil, Answer{Options: []string{"Go"}, Text: "Rust"}, true},
		{"no answer", false, nil, Answer{}, false},
		{"unknown option", false, nil, Answer{Options: []string{"Python"}}, false},
		{"single multiple", false, nil, Answer{Options: []string{"Go", "TypeScript"}}, false},
		{"single plus custom", false, nil, Answer{Options: []string{"Go"}, Text: "Rust"}, false},
		{"custom disabled", false, &no, Answer{Text: "Rust"}, false},
		{"duplicate", true, nil, Answer{Options: []string{"Go", "Go"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := base
			q.Multiple, q.Custom = tc.multiple, tc.custom
			content, err := FormatAnswers([]Question{q}, []Answer{tc.answer})
			if (err == nil) != tc.valid {
				t.Fatalf("content=%q error=%v", content, err)
			}
			if tc.valid && (!strings.HasPrefix(content, "1. "+q.Question+"\n\n") || strings.Contains(content, "Answer:")) {
				t.Fatalf("unreadable answer: %q", content)
			}
		})
	}
	if _, err := FormatAnswers([]Question{base}, nil); err == nil {
		t.Fatal("missing answers accepted")
	}
}
