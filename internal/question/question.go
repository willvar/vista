// Package question defines the structured choices shared by the Agent, worker,
// persistence boundary, and message submission API.
package question

import (
	"errors"
	"fmt"
	"strings"
)

type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type Question struct {
	Header   string   `json:"header,omitempty"`
	Question string   `json:"question"`
	Options  []Option `json:"options"`
	Multiple bool     `json:"multiple,omitempty"`
	Custom   *bool    `json:"custom,omitempty"`
}

type Answer struct {
	Options []string `json:"options"`
	Text    string   `json:"text,omitempty"`
}

type Reply struct {
	RunID   string   `json:"runId"`
	Answers []Answer `json:"answers"`
}

func (q Question) AllowsCustom() bool { return q.Custom == nil || *q.Custom }

func Validate(questions []Question) error {
	if len(questions) == 0 {
		return errors.New("at least one question is required")
	}
	for _, q := range questions {
		if strings.TrimSpace(q.Question) == "" {
			return errors.New("question text is required")
		}
		if len(q.Options) == 0 && !q.AllowsCustom() {
			return errors.New("a question must allow an option or a custom answer")
		}
		labels := make(map[string]bool, len(q.Options))
		for _, option := range q.Options {
			label := strings.TrimSpace(option.Label)
			if label == "" || labels[label] {
				return errors.New("question option labels must be nonempty and unique")
			}
			labels[label] = true
		}
	}
	return nil
}

// Format preserves the question and available choices in ordinary conversation
// history, even after the interactive pending state has been answered.
func Format(questions []Question) string {
	var lines []string
	for i, q := range questions {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, q.Question))
		for _, option := range q.Options {
			line := "- " + option.Label
			if option.Description != "" {
				line += ": " + option.Description
			}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func FormatAnswers(questions []Question, answers []Answer) (string, error) {
	if err := Validate(questions); err != nil {
		return "", err
	}
	if len(answers) != len(questions) {
		return "", errors.New("answer every question before submitting")
	}
	var lines []string
	for i, q := range questions {
		answer := answers[i]
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
		allowed := make(map[string]bool, len(q.Options))
		for _, option := range q.Options {
			allowed[option.Label] = true
		}
		seen := make(map[string]bool, len(answer.Options))
		for _, option := range answer.Options {
			if !allowed[option] || seen[option] {
				return "", errors.New("answer contains an unknown or repeated option")
			}
			seen[option] = true
		}
		selected := append([]string(nil), answer.Options...)
		if text != "" {
			selected = append(selected, text)
		}
		lines = append(lines, fmt.Sprintf("%d. %s\n\n%s", i+1, q.Question, strings.Join(selected, "; ")))
	}
	return strings.Join(lines, "\n\n"), nil
}
