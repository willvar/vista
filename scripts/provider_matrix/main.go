// Command provider_matrix writes a per-model verification worklist for the
// built-in OMP inventory. Its evidence labels are deliberately narrow:
// resolving or configuring a route does not certify a live Provider.
package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"vista/internal/modelcatalog"

	"vista/internal/provider"
)

func main() {
	if err := writeMatrix(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func writeMatrix(output io.Writer) error {
	writer := csv.NewWriter(output)
	if err := writer.Write([]string{
		"provider_id", "model_id", "model_package", "protocol_route", "default_endpoint", "wire_model_id",
		"tools_capability", "reasoning_capability", "input_modalities", "output_modalities", "context_limit", "input_limit", "output_limit", "catalog_status",
		"route_evidence", "configuration_evidence", "custom_endpoint_evidence", "auth_and_refresh", "stream_response", "tool_events", "reasoning_events", "errors_and_retry", "cancellation", "local_discovery", "live_model_status",
	}); err != nil {
		return err
	}
	for _, setup := range modelcatalog.ProviderSetups() {
		providerID := setup.ID
		if providerID == "custom" || modelcatalog.LocalProvider(providerID) {
			continue
		}
		for _, modelID := range provider.CatalogTextModelIDs(providerID) {
			entry, err := provider.ResolveCatalogModel(providerID, modelID)
			if err != nil {
				return fmt.Errorf("%s/%s: %w", providerID, modelID, err)
			}
			routeEvidence := "TestEveryOMPTextModelHasAProtocolRoute"
			configEvidence := "TestEveryBuiltInOMPTextModelLoadsThroughServer"
			endpointEvidence := "configuration_only"
			toolEvents := "not_applicable"
			if entry.Tools {
				toolEvents = "unverified_per_model"
			}
			reasoningEvents := "not_advertised"
			if entry.Reasoning {
				reasoningEvents = "unverified_per_model"
			}
			discovery := "not_applicable"
			wireID := entry.WireModelID
			if wireID == "" {
				wireID = modelID
			}
			if err := writer.Write([]string{
				providerID, modelID, entry.Package, entry.Protocol, entry.BaseURL, wireID,
				fmt.Sprint(entry.Tools), fmt.Sprint(entry.Reasoning), join(entry.InputModalities), join(entry.OutputModalities), optionalLimit(entry.ContextLimit), optionalLimit(entry.InputLimit), optionalLimit(entry.OutputLimit), entry.Status,
				routeEvidence, configEvidence, endpointEvidence, "unverified_per_model", "unverified_per_model", toolEvents, reasoningEvents, "unverified_per_model", "unverified_per_model", discovery, "unverified",
			}); err != nil {
				return err
			}
		}
	}
	writer.Flush()
	return writer.Error()
}

func join(values []string) string {
	if len(values) == 0 {
		return ""
	}
	result := values[0]
	for _, value := range values[1:] {
		result += "|" + value
	}
	return result
}

func optionalLimit(value int) string {
	if value <= 0 {
		return ""
	}
	return fmt.Sprint(value)
}
