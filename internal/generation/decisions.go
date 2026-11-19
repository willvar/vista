package generation

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"

	"vista/internal/model"
)

// Text evidence plus a bounded set of typed questions. The transport does not
// reinterpret a failed or malformed response as a negative judgment.
type Question struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	Instructions string   `json:"instructions"`
	Choices      []string `json:"choices,omitempty"`
}

func Decide(ctx context.Context, target model.GenerationTarget, state string, questions []Question) (json.RawMessage, error) {
	if target.Kind != model.DecisionOutput || strings.TrimSpace(state) == "" || len(questions) == 0 || len(questions) > 16 {
		return nil, errors.New("provide text evidence and 1–16 decision questions")
	}
	encoded := map[string]wire{}
	for _, q := range questions {
		if q.ID == "" || len(q.ID) > 64 || q.Instructions == "" || encoded[q.ID] != nil {
			return nil, errors.New("decision questions require unique IDs and instructions")
		}
		question := wire{"instructions": q.Instructions}
		switch q.Type {
		case "boolean":
			question["type"] = "noul"
		case "choice", "score":
			if len(q.Choices) < 2 || len(q.Choices) > 16 {
				return nil, errors.New("choices or score levels must contain 2–16 entries")
			}
			options := wire{}
			for _, s := range q.Choices {
				if strings.TrimSpace(s) == "" || options[s] != nil {
					return nil, errors.New("decision alternatives must be nonempty and unique")
				}
				options[s] = s
			}
			question["type"] = q.Type
			if q.Type == "choice" {
				question["criteria"] = options
			} else {
				question["criteria"] = q.Choices
			}
		default:
			return nil, errors.New("unsupported decision question type")
		}
		encoded[q.ID] = question
	}
	c, err := newRequest(ctx, target)
	if err != nil {
		return nil, err
	}
	out, err := c.decideNative(state, questions, encoded)
	if err != nil {
		return nil, err
	}
	if err := validateDecisionResult(c.target.Model.ProviderID, out, questions); err != nil {
		return nil, err
	}

	return json.Marshal(out)
}

func validateDecisionResult(adapter string, out wire, questions []Question) error {
	var answers wire
	switch a := out["answers"].(type) {
	case wire:
		answers = a
	case map[string]any:
		answers = wire(a)
	}
	if len(answers) != len(questions) {
		return errors.New("decision response did not answer the submitted questions")
	}
	for _, q := range questions {
		answer := answers[q.ID]
		kind := text(answer, "type")
		// Only the native codecs may mint these normalized outcomes. A failed
		// question is distinguishable from a successful negative judgment.
		if out["providerResult"] != nil && (adapter == "openai" || adapter == "clawrouter" || adapter == "levanto") && (kind == "error" || kind == "abstained") {
			continue
		}
		expected := q.Type
		if expected == "boolean" {
			expected = "noul"
		}
		if kind != "" && kind != expected {
			return errors.New("decision response has an unexpected answer type")
		}
		switch q.Type {
		case "boolean":
			if !decisionNumberIn(getValue(answer, "noul"), 1) {
				return errors.New("decision did not return a valid Boolean probability")
			}
		case "choice":
			if !slices.Contains(q.Choices, text(answer, "choice")) {
				return errors.New("decision returned an invalid choice")
			}
		case "score":
			if !decisionNumberIn(getValue(answer, "score"), float64(len(q.Choices)-1)) {
				return errors.New("decision returned an invalid score")
			}
			if adapter == "google" || adapter == "subq" {
				legend, _ := getValue(answer, "legend").(map[string]any)
				if len(legend) != len(q.Choices) {
					return errors.New("decision score rubric does not match the request")
				}
				for i, description := range q.Choices {
					if legend[strconv.Itoa(i)] != description {
						return errors.New("decision score rubric does not match the request")
					}
				}
			}
		}
	}
	return nil
}
