package server

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	modelcfg "vista/internal/model"
	"vista/internal/store"
)

func TestConnectionStatisticsAPIRequiresConfiguredConnection(t *testing.T) {
	a := selectionApp(t)
	for query, status := range map[string]int{"key=manual-luna&days=30": 200, "key=missing&days=30": 404, "key=manual-luna&days=400": 400, "days=30": 400} {
		response := httptest.NewRecorder()
		a.connectionStatistics(response, httptest.NewRequest(http.MethodGet, "/api/connection-statistics?"+query, nil))
		if response.Code != status {
			t.Fatalf("%s: HTTP %d: %s", query, response.Code, response.Body.String())
		}
	}
}

func TestConnectionStatisticsIncludesLegacyModelKeySamples(t *testing.T) {
	a := selectionApp(t)
	sample := modelcfg.CallUsage{ID: uuid.NewString(), Connection: "model:manual-luna", Model: "gpt-6-luna", Route: strings.Repeat("a", 64), Outcome: "succeeded", FirstTokenMS: new(125.0)}
	if _, err := a.store.RecordCallUsage(context.Background(), sample, time.Now()); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	a.connectionStatistics(response, httptest.NewRequest(http.MethodGet, "/api/connection-statistics?key=manual-luna&days=30", nil))
	var result struct {
		Groups []store.ConnectionUsageGroup `json:"groups"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != 200 || len(result.Groups) != 1 || result.Groups[0].Requests != 1 || result.Groups[0].FirstTokenMS == nil || *result.Groups[0].FirstTokenMS != 125 {
		t.Fatalf("legacy connection usage: HTTP %d %+v %v", response.Code, result, err)
	}
}
