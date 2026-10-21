package provider

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"
)

func modelsDevRecentlyReplaced(current *modelsDevCatalog, revision string, now time.Time) bool {
	if current != nil {
		for _, previous := range current.History {
			if previous.Revision == revision && previous.ReplacedAt.Add(modelsDevHistoryWindow).After(now) {
				return true
			}
		}
	}
	return false
}

type modelsDevChange struct {
	ProviderID string
	ModelID    string
	Field      string
	Before     any
	After      any
}

// Confirm the changed fields, not a whole-catalog digest. Unrelated additions
// and corrections on another mirror do not invalidate evidence for this row.
func modelsDevChangesToConfirm(current, candidate *modelsDevCatalog) []modelsDevChange {
	if current == nil {
		return nil
	}
	changed := 0
	var all, risky []modelsDevChange
	for _, providerID := range slices.Sorted(maps.Keys(current.Providers)) {
		models := current.Providers[providerID]
		next := candidate.Providers[providerID]
		for _, id := range slices.Sorted(maps.Keys(models)) {
			before := models[id]
			after, ok := next[id]
			if !ok {
				change := modelsDevChange{providerID, id, "presence", true, false}
				all, risky = append(all, change), append(risky, change)
				continue
			}
			if reflect.DeepEqual(before, after) {
				continue
			}
			previous, updated := modelsDevModelFields(before), modelsDevModelFields(after)
			rowChanged := false
			for _, field := range slices.Sorted(maps.Keys(previous)) {
				if reflect.DeepEqual(previous[field], updated[field]) {
					continue
				}
				rowChanged = true
				change := modelsDevChange{providerID, id, field, previous[field], updated[field]}
				all = append(all, change)
				needsConfirmation := false
				switch field {
				case "api":
					needsConfirmation = true
				case "reasoning":
					needsConfirmation = before.Reasoning && !after.Reasoning
				case "supportsTools":
					needsConfirmation = before.SupportsTools != nil && (after.SupportsTools == nil || *before.SupportsTools && !*after.SupportsTools)
				case "input", "thinking.efforts":
					for _, value := range previous[field].([]string) {
						if !slices.Contains(updated[field].([]string), value) {
							needsConfirmation = true
						}
					}
				case "contextWindow":
					needsConfirmation = modelsDevLimitChanged(before.ContextWindow, after.ContextWindow)
				case "maxTokens":
					needsConfirmation = modelsDevLimitChanged(before.MaxTokens, after.MaxTokens)
				}
				if needsConfirmation {
					risky = append(risky, change)
				}
			}
			if rowChanged {
				changed++
			}
		}
	}
	// Ten or more changed existing rows covering at least a quarter of the
	// installed catalog warrant confirmation. Additions do not count here.
	if changed >= 10 && changed*4 >= current.ModelCount {
		return all
	}
	return risky
}

func modelsDevLimitChanged(before, after int) bool {
	return before > 0 && (after == 0 || float64(after) >= 2*float64(before) || float64(after) <= float64(before)/2)
}

func modelsDevModelFields(model OMPModel) map[string]any {
	thinking := model.Thinking
	if thinking == nil {
		thinking = &OMPThinking{}
	}
	var tools any
	if model.SupportsTools != nil {
		tools = *model.SupportsTools
	}
	return map[string]any{
		"name": model.Name, "api": model.API, "reasoning": model.Reasoning,
		"input":         slices.Sorted(slices.Values(model.Input)),
		"contextWindow": model.ContextWindow, "maxTokens": model.MaxTokens, "supportsTools": tools,
		"thinking.efforts": slices.Sorted(slices.Values(thinking.Efforts)),
		"thinking.mode":    thinking.Mode, "thinking.defaultLevel": thinking.DefaultLevel,
		"thinking.effortMap": thinking.EffortMap, "thinking.supportsDisplay": thinking.SupportsDisplay,
		"thinking.requiresEffort": thinking.RequiresEffort,
	}
}

func (change modelsDevChange) valueIn(catalog *modelsDevCatalog) any {
	model, present := catalog.Providers[change.ProviderID][change.ModelID]
	if change.Field == "presence" {
		return present
	}
	if !present {
		return nil
	}
	return modelsDevModelFields(model)[change.Field]
}

func modelsDevValue(value any) string {
	if value == nil {
		return "unknown"
	}
	data, _ := json.Marshal(value)
	return string(data)
}

func confirmModelsDevChanges(changes []modelsDevChange, witnesses map[string]*modelsDevCatalog) error {
	var pending []string
	for _, change := range changes {
		confirmed := false
		var observations []string
		for _, url := range slices.Sorted(maps.Keys(witnesses)) {
			value := change.valueIn(witnesses[url])
			if reflect.DeepEqual(value, change.After) {
				confirmed = true
				break
			}
			observations = append(observations, fmt.Sprintf("%s has %s", url, modelsDevValue(value)))
		}
		if !confirmed {
			if len(observations) == 0 {
				observations = []string{"no independent source available"}
			}
			pending = append(pending, fmt.Sprintf("%s/%s %s: %s -> %s (%s)", change.ProviderID, change.ModelID,
				change.Field, modelsDevValue(change.Before), modelsDevValue(change.After), strings.Join(observations, "; ")))
		}
	}
	if len(pending) == 0 {
		return nil
	}
	const preview = 6
	details := strings.Join(pending[:min(preview, len(pending))], "\n")
	if len(pending) > preview {
		details += fmt.Sprintf("\n... and %d more unconfirmed changes", len(pending)-preview)
	}
	return fmt.Errorf("could not confirm %d metadata changes:\n%s", len(pending), details)
}
