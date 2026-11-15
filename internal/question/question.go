// Package question defines the structured choices shared by the Agent, worker,
// persistence boundary, and message submission API.
package question

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type Option struct {
	ID          string `json:"id,omitempty"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type Condition struct {
	QuestionID string `json:"questionId"`
	OptionID   string `json:"optionId"`
}

type Question struct {
	ID       string     `json:"id,omitempty"`
	Header   string     `json:"header,omitempty"`
	Question string     `json:"question"`
	Options  []Option   `json:"options"`
	Multiple bool       `json:"multiple,omitempty"`
	Custom   *bool      `json:"custom,omitempty"`
	When     *Condition `json:"when,omitempty"`
}

type Answer struct {
	QuestionID string   `json:"questionId,omitempty"`
	Options    []string `json:"options"`
	Text       string   `json:"text,omitempty"`
}

type Reply struct {
	RunID   string   `json:"runId"`
	CallID  string   `json:"callId,omitempty"`
	Answers []Answer `json:"answers"`
}

func (q Question) AllowsCustom() bool { return q.Custom == nil || *q.Custom }

var identifier = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// Identified distinguishes the keyed contract from unchanged positional histories.
func Identified(questions []Question) bool {
	for _, q := range questions {
		if q.ID != "" || q.When != nil {
			return true
		}
		for _, option := range q.Options {
			if option.ID != "" {
				return true
			}
		}
	}
	return false
}

// MatchesCall permits an omitted call ID only for legacy positional clients.
func MatchesCall(questions []Question, expected, received string) bool {
	return received == expected || received == "" && !Identified(questions)
}

func Validate(questions []Question) error {
	if len(questions) == 0 {
		return errors.New("at least one question is required")
	}
	identified := Identified(questions)
	previous := make(map[string]Question, len(questions))
	for i, q := range questions {
		path := fmt.Sprintf("questions[%d]", i)
		if strings.TrimSpace(q.Question) == "" {
			return fmt.Errorf("%s.question: question text is required", path)
		}
		if len(q.Options) == 0 && !q.AllowsCustom() {
			return fmt.Errorf("%s: allow an option or a custom answer", path)
		}
		if identified {
			if !identifier.MatchString(q.ID) {
				return fmt.Errorf("%s.id: use a unique ID starting with a letter, followed by letters, digits, _ or -; identified prompts require IDs on every question and option", path)
			}
			if _, exists := previous[q.ID]; exists {
				return fmt.Errorf("%s.id: duplicate question ID %q", path, q.ID)
			}
		}
		labels, ids := make(map[string]bool), make(map[string]bool)
		for j, option := range q.Options {
			label := strings.TrimSpace(option.Label)
			if label == "" || labels[label] {
				return fmt.Errorf("%s.options[%d].label: labels must be nonempty and unique", path, j)
			}
			labels[label] = true
			if identified {
				if !identifier.MatchString(option.ID) || ids[option.ID] {
					return fmt.Errorf("%s.options[%d].id: use a unique option ID starting with a letter, followed by letters, digits, _ or -", path, j)
				}
				ids[option.ID] = true
			}
		}
		if q.When != nil {
			parent, exists := previous[q.When.QuestionID]
			if !exists {
				return fmt.Errorf("%s.when.questionId: %q must identify an earlier question; split questions into separate calls when the follow-up is not yet known", path, q.When.QuestionID)
			}
			if parent.Multiple {
				return fmt.Errorf("%s.when.questionId: conditions must reference a single-choice question", path)
			}
			found := false
			for _, option := range parent.Options {
				found = found || option.ID == q.When.OptionID
			}
			if !found {
				return fmt.Errorf("%s.when.optionId: %q is not an option of question %q", path, q.When.OptionID, parent.ID)
			}
		}
		previous[q.ID] = q
	}
	return nil
}

// Format preserves the complete prompt and its conditions in ordinary history.
// Legacy formatting stays byte-identical so saved pending projections still match.
func Format(questions []Question) string {
	var lines []string
	for i, q := range questions {
		if i > 0 {
			lines = append(lines, "")
		}
		title := fmt.Sprintf("%d. %s", i+1, q.Question)
		if q.ID != "" {
			title += " [" + q.ID + "]"
		}
		lines = append(lines, title)
		if q.When != nil {
			lines = append(lines, fmt.Sprintf("Only when %s = %s (and that question is active).", q.When.QuestionID, q.When.OptionID))
		}
		for _, option := range q.Options {
			line := "- " + option.Label
			if option.ID != "" {
				line += " [" + option.ID + "]"
			}
			if option.Description != "" {
				line += ": " + option.Description
			}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func formatAnswer(q Question, answer Answer) (string, error) {
	text := strings.TrimSpace(answer.Text)
	count := len(answer.Options)
	if text != "" {
		if !q.AllowsCustom() {
			return "", errors.New("this question does not accept a custom answer")
		}
		count++
	}
	if count == 0 || !q.Multiple && count != 1 {
		return "", errors.New("select an answer; single-choice questions accept exactly one answer")
	}
	allowed := make(map[string]Option, len(q.Options))
	for _, option := range q.Options {
		key := option.Label
		if q.ID != "" {
			key = option.ID
		}
		allowed[key] = option
	}
	seen := make(map[string]bool, len(answer.Options))
	selected := make([]string, 0, count)
	for _, key := range answer.Options {
		option, exists := allowed[key]
		if !exists || seen[key] {
			return "", errors.New("answer contains an unknown or repeated option")
		}
		seen[key] = true
		label := option.Label
		if q.ID != "" {
			label += " [" + option.ID + "]"
		}
		selected = append(selected, label)
	}
	if text != "" {
		selected = append(selected, text)
	}
	return strings.Join(selected, "; "), nil
}

func FormatAnswers(questions []Question, answers []Answer) (string, error) {
	if err := Validate(questions); err != nil {
		return "", err
	}
	identified := Identified(questions)
	byID := make(map[string]Answer, len(answers))
	if identified {
		known := make(map[string]bool, len(questions))
		for _, q := range questions {
			known[q.ID] = true
		}
		for _, answer := range answers {
			if !known[answer.QuestionID] {
				return "", fmt.Errorf("answers.questionId: unknown or missing question ID %q", answer.QuestionID)
			}
			if _, exists := byID[answer.QuestionID]; exists {
				return "", fmt.Errorf("answers.questionId: duplicate answer for %q", answer.QuestionID)
			}
			byID[answer.QuestionID] = answer
		}
	} else if len(answers) != len(questions) {
		return "", errors.New("answer every question before submitting")
	}
	active := make(map[string]bool, len(questions))
	var lines []string
	for i, q := range questions {
		var answer Answer
		if identified {
			var exists bool
			answer, exists = byID[q.ID]
			enabled := q.When == nil
			if q.When != nil {
				parent := byID[q.When.QuestionID]
				enabled = active[q.When.QuestionID] && strings.TrimSpace(parent.Text) == "" && len(parent.Options) == 1 && parent.Options[0] == q.When.OptionID
			}
			active[q.ID] = enabled
			if !enabled {
				if exists {
					return "", fmt.Errorf("answers[%s]: this question is inactive; do not submit answers from hidden branches", q.ID)
				}
				continue
			}
			if !exists {
				return "", fmt.Errorf("answers[%s]: answer this active question before submitting", q.ID)
			}
		} else {
			answer = answers[i]
			if answer.QuestionID != "" {
				return "", errors.New("legacy positional answers must not contain question IDs")
			}
		}
		selected, err := formatAnswer(q, answer)
		if err != nil {
			return "", fmt.Errorf("answers[%d] (%s): %w", i, q.ID, err)
		}
		title := q.Question
		if identified {
			title += " [" + q.ID + "]"
		}
		lines = append(lines, fmt.Sprintf("%d. %s\n\n%s", len(lines)+1, title, selected))
	}
	return strings.Join(lines, "\n\n"), nil
}
