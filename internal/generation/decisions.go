package generation

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
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
			question["criteria"] = wire{"true": "The condition is met", "false": "The condition is not met"}
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
	out, err := c.api(http.MethodPost, c.route("systemone"), wire{"model": c.model, "state": state, "questions": encoded}, nil)
	if err != nil {
		return nil, err
	}
	for _, q := range questions {
		answer := getValue(out, "answers", q.ID)
		switch q.Type {
		case "boolean":
			n, ok := getValue(answer, "noul").(json.Number)
			p, e := n.Float64()
			if !ok || e != nil || math.IsNaN(p) || p < 0 || p > 1 {
				return nil, errors.New("decision did not return a valid boolean probability")
			}
		case "choice":
			v := text(answer, "choice")
			found := false
			for _, s := range q.Choices {
				if s == v {
					found = true
					break
				}
			}
			if !found {
				return nil, errors.New("decision returned an invalid choice")
			}
		case "score":
			n, ok := getValue(answer, "score").(json.Number)
			p, e := n.Float64()
			if !ok || e != nil || math.IsNaN(p) || p < 0 || p > float64(len(q.Choices)-1) {
				return nil, errors.New("decision returned an invalid score")
			}
		}
	}
	return json.Marshal(out)
}
