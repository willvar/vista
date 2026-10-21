package server

import (
	"strings"
	"testing"
)

func TestSearchCredentialsFitWorkerCommandBudget(t *testing.T) {
	t.Setenv("VISTA_WEBSEARCH_EXA_API_KEY", strings.Repeat("<", 4096))
	if _, err := searchProviders(); err != nil {
		t.Fatalf("accepted credential rejected: %v", err)
	}
	t.Setenv("VISTA_WEBSEARCH_EXA_API_KEY", strings.Repeat("<", 4097))
	if _, err := searchProviders(); err == nil {
		t.Fatal("oversized credential accepted")
	}
}
