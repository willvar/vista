package generation

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"vista/internal/model"
)

// The operator selects a saved connection and model, not a wire dialect.
// Keep the native request and response boundaries here, rather than passing a
// decision model through a conversation protocol or synthesizing an answer.
func (c request) decideNative(state string, questions []Question, systemOne map[string]wire) (wire, error) {
	id := c.target.Model.ProviderID
	switch id {
	case "levanto":
		return c.sageDecision(state, questions)
	case "openai", "clawrouter":
		return c.openAIDecision(state, questions)
	case "google":
		for _, q := range questions {
			if strings.IndexFunc(q.ID, func(r rune) bool {
				return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_')
			}) >= 0 {
				return nil, errors.New("Gemini decision question IDs use letters, numbers, hyphens or underscores")
			}
		}
		return c.api(http.MethodPost, c.route("decisions"), wire{"model": c.model, "input": state, "questions": systemOne}, nil)
	case "subq":
		for _, q := range questions {
			if q.Type == "score" && len(q.Choices) > 10 {
				return nil, errors.New("SubQ supports at most ten score levels")
			}
		}
	}
	return c.api(http.MethodPost, c.route("systemone"), wire{"model": c.model, "state": state, "questions": systemOne}, nil)
}

func (c request) openAIDecision(state string, questions []Question) (wire, error) {
	encoded := make([]wire, 0, len(questions))
	for _, q := range questions {
		item := wire{"name": q.ID, "type": q.Type, "instructions": q.Instructions}
		switch q.Type {
		case "boolean":
			item["type"] = "predicate"
		case "choice":
			items := make([]wire, 0, len(q.Choices))
			for _, name := range q.Choices {
				items = append(items, wire{"value": name, "description": name})
			}
			item["choices"] = items
		case "score":
			items := make([]wire, 0, len(q.Choices))
			for i, label := range q.Choices {
				items = append(items, wire{"label": strconv.Itoa(i), "description": label})
			}
			item["levels"] = items
		}
		encoded = append(encoded, item)
	}
	path, modelID := "decisions", c.model
	if c.target.Model.ProviderID == "clawrouter" {
		// The catalog is checked on the same saved service before executing;
		// it can authorize only the reviewed OpenAI dialect, never another URL.
		allowed, err := c.clawRouterAllows(model.DecisionOutput, "")
		if err != nil {
			return nil, err
		}
		if !allowed || c.model != "openai/gpt-6-luna" {
			return nil, errors.New("the selected ClawRouter connection did not advertise GPT-6 Luna decisions")
		}
		path, modelID = "native/openai/v1/decisions", "gpt-6-luna"
	}
	out, err := c.api(http.MethodPost, c.route(path), wire{"model": modelID, "input": state, "questions": encoded}, nil)
	if err != nil {
		return nil, err
	}
	rows, ok := out["answers"].([]any)
	if !ok || len(rows) != len(questions) || text(out, "model") == "" {
		return nil, errors.New("OpenAI decision response did not answer the submitted questions")
	}
	answers := wire{}
	for i, q := range questions {
		row, _ := rows[i].(map[string]any)
		if text(row, "name") != q.ID {
			return nil, errors.New("OpenAI decision response has an unexpected question name")
		}
		if text(row, "type") == "refusal" {
			answers[q.ID] = wire{"type": "error", "code": "refusal"}
			continue
		}
		if q.Type == "boolean" && text(row, "type") == "predicate" {
			answers[q.ID] = wire{"type": "noul", "noul": row["probability"]}
			continue
		}
		if q.Type == "boolean" || text(row, "type") != q.Type {
			return nil, errors.New("OpenAI decision response has an unexpected answer type")
		}
		items, _ := row["probabilities"].([]any)
		if len(items) != len(q.Choices) || !decisionNumberIn(row["confidence"], 1) {
			return nil, errors.New("OpenAI decision response has an invalid probability distribution")
		}
		probabilities := wire{}
		nonzero := false
		for _, item := range items {
			key := text(item, "value")
			if q.Type == "score" {
				key = text(item, "label")
				idx, err := strconv.Atoi(key)
				value, _ := getValue(item, "value").(json.Number)
				if err != nil || idx < 0 || idx >= len(q.Choices) || value.String() != strconv.Itoa(idx) || key != strconv.Itoa(idx) {
					return nil, errors.New("OpenAI decision returned an unknown rubric level")
				}
			} else if !slices.Contains(q.Choices, key) {
				return nil, errors.New("OpenAI decision returned an unknown choice")
			}
			p := getValue(item, "probability")
			if _, duplicate := probabilities[key]; duplicate || !decisionNumberIn(p, 1) {
				return nil, errors.New("OpenAI decision response has an invalid probability")
			}
			probabilities[key] = p
			n, _ := p.(json.Number).Float64()
			nonzero = nonzero || n > 0
		}
		if !nonzero {
			return nil, errors.New("OpenAI decision returned only zero probabilities")
		}
		answers[q.ID] = wire{"type": q.Type, q.Type: row[q.Type], "confidence": row["confidence"], "probabilities": probabilities}
	}
	return wire{"model": out["model"], "answers": answers, "providerResult": out}, nil
}

func (c request) sageDecision(state string, questions []Question) (wire, error) {
	encoded := make([]wire, 0, len(questions))
	for _, q := range questions {
		item := wire{"id": q.ID, "instructions": q.Instructions}
		switch q.Type {
		case "boolean":
			item["kind"] = "yesno"
		case "choice":
			item["kind"] = "choice"
			values := make([]wire, 0, len(q.Choices))
			for _, choice := range q.Choices {
				values = append(values, wire{"option": choice, "description": choice})
			}
			item["options"] = values
		case "score":
			item["kind"] = "scale"
			values := make([]wire, 0, len(q.Choices))
			for i, description := range q.Choices {
				values = append(values, wire{"level": i, "description": description})
			}
			item["levels"] = values
		}
		encoded = append(encoded, item)
	}
	path, request := "decide", wire{"content": state, "question": encoded[0]}
	if len(questions) > 1 {
		path, request = "decide/batch", wire{"requests": []wire{{"content": state, "questions": encoded}}}
	}
	out, err := c.api(http.MethodPost, c.route(path), request, nil)
	if err != nil {
		return nil, err
	}
	var rows = []any{out}
	if len(questions) > 1 {
		groups, _ := out["results"].([]any)
		if len(groups) != 1 {
			return nil, errors.New("Sage decision returned an unexpected batch")
		}
		rows, _ = getValue(groups[0], "answers").([]any)
	}
	if len(rows) != len(questions) {
		return nil, errors.New("Sage decision response did not answer the submitted questions")
	}
	answers := wire{}
	for i, q := range questions {
		row := rows[i]
		if len(questions) > 1 {
			ok, valid := getValue(row, "ok").(bool)
			if !valid {
				return nil, errors.New("Sage decision returned malformed answer status")
			}
			if !ok {
				answers[q.ID] = wire{"type": "error", "code": "provider-error"}
				continue
			}
			row = getValue(row, "result")
		}
		result, _ := getValue(row, "result").(map[string]any)
		if text(row, "id") != q.ID || text(row, "kind") != encoded[i]["kind"] || result == nil {
			return nil, errors.New("Sage decision returned an unexpected question or type")
		}
		switch q.Type {
		case "boolean":
			v, exists := result["answer"]
			if !exists || (v != nil && v != "yes" && v != "no") || !decisionNumberIn(result["probability"], 1) {
				return nil, errors.New("Sage decision returned an invalid Boolean judgment")
			}
			if v == nil {
				answers[q.ID] = wire{"type": "abstained"}
			} else {
				answers[q.ID] = wire{"type": "noul", "noul": result["probability"], "answer": v == "yes"}
			}
		case "choice":
			if v, exists := result["chosen"]; exists && v == nil {
				answers[q.ID] = wire{"type": "abstained"}
			} else {
				answers[q.ID] = wire{"type": "choice", "choice": v}
			}
		case "score":
			answers[q.ID] = wire{"type": "score", "score": result["expectation"]}
		}
	}
	return wire{"model": c.model, "answers": answers, "providerResult": out}, nil
}

func decisionNumberIn(v any, max float64) bool {
	n, ok := v.(json.Number)
	if !ok {
		return false
	}
	p, err := n.Float64()
	return err == nil && !math.IsNaN(p) && !math.IsInf(p, 0) && p >= 0 && p <= max
}
