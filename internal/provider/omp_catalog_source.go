package provider

import (
	"encoding/json"
	"errors"
	"fmt"
)

type ompSourceModel struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	OMPModel
}

func decodeOMPSourceModels(data []byte) (map[string]map[string]ompSourceModel, error) {
	var compact ompCompactSource
	if err := json.Unmarshal(data, &compact); err != nil {
		return nil, err
	}
	if compact.Version != 1 {
		return nil, fmt.Errorf("unsupported OMP catalog version %d (expected 1)", compact.Version)
	}
	return compact.models()
}

// OMP's version-1 wire format interns objects and arrays in nodes, factors out
// provider constants, and stores booleans in bitmaps. See model-catalog-format.ts
// at blob df9ceaa58e3de5a42ae3c1d3a4c788380aa431f0 (MIT, licenses/oh-my-pi).
// Compat bases/deltas concern OMP's transports and are deliberately not applied.
type ompCompactSource struct {
	Version       int      `json:"version"`
	Keys          []string `json:"keys"`
	BooleanShapes [][]int  `json:"booleanShapes"`
	Nodes         [][]any  `json:"nodes"`
	Providers     [][]any  `json:"providers"`
	values        []any
	visiting      []bool
}

func (source *ompCompactSource) models() (map[string]map[string]ompSourceModel, error) {
	if len(source.Providers) == 0 || len(source.Providers) > 256 {
		return nil, errors.New("invalid compact OMP provider count")
	}
	source.values = make([]any, len(source.Nodes))
	source.visiting = make([]bool, len(source.Nodes))
	providers := make(map[string]map[string]ompSourceModel, len(source.Providers))
	count := 0
	for _, provider := range source.Providers {
		if len(provider) != 3 {
			return nil, errors.New("invalid compact OMP provider row")
		}
		providerID, ok := provider[0].(string)
		if !ok || providerID == "" || providers[providerID] != nil {
			return nil, errors.New("invalid or duplicate compact OMP provider ID")
		}
		constants, err := source.object(provider[1])
		if err != nil {
			return nil, err
		}
		rows, ok := provider[2].([]any)
		if !ok || len(rows) > 20000-count {
			return nil, errors.New("invalid compact OMP model count")
		}
		count += len(rows)
		models := make(map[string]ompSourceModel, len(rows))
		for _, row := range rows {
			row, ok := row.([]any)
			if !ok || (len(row) != 2 && len(row) != 3) {
				return nil, errors.New("invalid compact OMP model row")
			}
			id, ok := row[0].(string)
			if !ok || id == "" {
				return nil, errors.New("invalid compact OMP model ID")
			}
			if _, exists := models[id]; exists {
				return nil, errors.New("duplicate compact OMP model ID")
			}
			fields, err := source.object(row[1])
			if err != nil {
				return nil, err
			}
			// Copy only Vista's stable subset, without modifying shared nodes or
			// serializing OMP-only pricing, headers, compat, or runtime state.
			model := map[string]any{"id": id}
			for _, key := range []string{"id", "provider", "name", "api", "reasoning", "input", "contextWindow", "maxTokens", "supportsTools", "thinking"} {
				if value, exists := constants[key]; exists {
					model[key] = value
				}
				if value, exists := fields[key]; exists {
					model[key] = value
				}
			}
			data, err := json.Marshal(model)
			if err != nil {
				return nil, err
			}
			var decoded ompSourceModel
			if err := json.Unmarshal(data, &decoded); err != nil {
				return nil, err
			}
			models[id] = decoded
		}
		providers[providerID] = models
	}
	return providers, nil
}

func (source *ompCompactSource) object(value any) (map[string]any, error) {
	value, err := source.value(value, 0)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("compact OMP row must reference an object")
	}
	return object, nil
}

func ompCompactIndex(value any, size int) (int, bool) {
	number, ok := value.(float64)
	if !ok || number < 0 || number >= float64(size) || number != float64(int(number)) {
		return 0, false
	}
	return int(number), true
}

func (source *ompCompactSource) value(value any, depth int) (any, error) {
	ref, ok := value.([]any)
	if !ok {
		switch value.(type) {
		case nil, string, float64, bool:
			return value, nil
		default:
			return nil, errors.New("invalid compact OMP value")
		}
	}
	if len(ref) != 1 {
		return nil, errors.New("invalid compact OMP node reference")
	}
	if ref[0] == float64(-1) { // OMP's explicitly present undefined value.
		return nil, nil
	}
	id, ok := ompCompactIndex(ref[0], len(source.Nodes))
	if !ok {
		return nil, errors.New("compact OMP node reference is out of bounds")
	}
	if depth >= 64 || source.visiting[id] {
		return nil, errors.New("compact OMP nodes are cyclic or nested too deeply")
	}
	if source.values[id] != nil {
		return source.values[id], nil
	}
	source.visiting[id] = true
	defer func() { source.visiting[id] = false }()
	node := source.Nodes[id]
	if len(node) != 2 && len(node) != 4 {
		return nil, errors.New("invalid compact OMP node")
	}
	entries, ok := node[1].([]any)
	if !ok {
		return nil, errors.New("invalid compact OMP node entries")
	}
	var result any
	switch {
	case node[0] == float64(1) && len(node) == 2:
		array := make([]any, len(entries))
		for i, entry := range entries {
			decoded, err := source.value(entry, depth+1)
			if err != nil {
				return nil, err
			}
			array[i] = decoded
		}
		result = array
	case node[0] == float64(0) && len(node) == 4:
		shape, ok := ompCompactIndex(node[2], len(source.BooleanShapes))
		if !ok || len(entries)%2 != 0 {
			return nil, errors.New("invalid compact OMP object shape")
		}
		keys := source.BooleanShapes[shape]
		masks, ok := node[3].([]any)
		if !ok || len(masks) != (len(keys)+31)/32 {
			return nil, errors.New("invalid compact OMP boolean masks")
		}
		object := make(map[string]any, len(entries)/2+len(keys))
		for i := 0; i < len(entries); i += 2 {
			key, ok := ompCompactIndex(entries[i], len(source.Keys))
			if !ok {
				return nil, errors.New("compact OMP key is out of bounds")
			}
			decoded, err := source.value(entries[i+1], depth+1)
			if err != nil {
				return nil, err
			}
			object[source.Keys[key]] = decoded
		}
		for i, key := range keys {
			if key < 0 || key >= len(source.Keys) {
				return nil, errors.New("compact OMP boolean key is out of bounds")
			}
			mask, ok := masks[i/32].(float64)
			if !ok || mask < 0 || mask > 4294967295 || mask != float64(uint32(mask)) {
				return nil, errors.New("invalid compact OMP boolean mask")
			}
			object[source.Keys[key]] = uint32(mask)&(1<<uint(i%32)) != 0
		}
		result = object
	default:
		return nil, errors.New("invalid compact OMP node type")
	}
	source.values[id] = result
	return result, nil
}
