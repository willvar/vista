package store

import (
	"context"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
	modelcfg "vista/internal/model"
)

func TestConnectionStatisticsSeparatesRoutesAndRetainsMissingTiming(t *testing.T) {
	s := selectionStore(t)
	ctx := context.Background()
	now := time.Now()
	sample := modelcfg.CallUsage{ID: uuid.NewString(), Connection: "connection:one", Model: "gpt-6-astra", Route: strings.Repeat("a", 64), Outcome: "succeeded", FirstTokenMS: new(260.0), RequestedTier: "ultrafast", ActualTier: "standard"}
	if ok, err := s.RecordCallUsage(ctx, sample, now); err != nil || !ok {
		t.Fatalf("record: %v %v", ok, err)
	}
	if ok, err := s.RecordCallUsage(ctx, sample, now); err != nil || ok {
		t.Fatalf("dedupe: %v %v", ok, err)
	}
	sample.ID = uuid.NewString()
	sample.ProxyID = "proxy"
	sample.Route = strings.Repeat("b", 64)
	sample.FirstTokenMS = nil
	sample.Outcome = "limited"
	if _, err := s.RecordCallUsage(ctx, sample, now); err != nil {
		t.Fatal(err)
	}
	sample.ID = uuid.NewString()
	sample.Connection = "connection:other"
	if _, err := s.RecordCallUsage(ctx, sample, now); err != nil {
		t.Fatal(err)
	}
	groups, _, err := s.ConnectionUsage(ctx, "connection:one", 30, now)
	if err != nil || len(groups) != 2 || groups[0].Requests != 1 || groups[0].FirstTokenSamples != 1 || groups[0].FirstTokenMS == nil || *groups[0].FirstTokenMS != 260 || groups[1].FirstTokenMS != nil || groups[1].FirstTokenSamples != 0 {
		t.Fatalf("unexpected aggregation: %+v %v", groups, err)
	}
	if err := s.ResetStatistics(ctx, now); err != nil {
		t.Fatal(err)
	}
	groups, _, err = s.ConnectionUsage(ctx, "connection:one", 30, now)
	if err != nil || len(groups) != 0 {
		t.Fatalf("reset retained samples: %+v %v", groups, err)
	}
}
