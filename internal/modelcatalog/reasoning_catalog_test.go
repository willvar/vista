package modelcatalog

import (
	"testing"

	"vista/internal/provider"
	"vista/internal/testcatalog"
)

func useExplicitReasoningCatalog(t *testing.T) {
	t.Helper()
	original := provider.CurrentOMPCatalog()
	t.Cleanup(original.Activate)
	testcatalog.Install(t, provider.LoadOMPCatalog)
}
