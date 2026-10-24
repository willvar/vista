// Package testcatalog installs explicit reasoning metadata for protocol and
// selection tests. Its fixture records models.dev controls from 2026-10-24;
// identity, routes and unrelated limits keep the bundled OMP test baseline.
package testcatalog

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

//go:embed testdata/reasoning.json
var reasoning []byte

func Install(t *testing.T, load func(string) error) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	base, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../provider/omp_models.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog map[string]json.RawMessage
	if err := json.Unmarshal(base, &catalog); err != nil {
		t.Fatal(err)
	}
	var rows map[string]map[string]json.RawMessage
	if err := json.Unmarshal(reasoning, &rows); err != nil {
		t.Fatal(err)
	}
	normalized, _ := json.Marshal(rows)
	digest := sha256.Sum256(normalized)
	count := 0
	for _, models := range rows {
		count += len(models)
	}
	catalog["modelsDev"], _ = json.Marshal(struct {
		Schema     int                                   `json:"schema"`
		Revision   string                                `json:"revision"`
		ModelCount int                                   `json:"modelCount"`
		Providers  map[string]map[string]json.RawMessage `json:"providers"`
	}{1, hex.EncodeToString(digest[:]), count, rows})
	data, _ := json.Marshal(catalog)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "omp-models.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := load(dir); err != nil {
		t.Fatal(err)
	}
}
