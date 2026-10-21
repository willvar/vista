package server

import (
	"errors"
	"math"
	"strings"
	"time"

	"vista/internal/store"
	"vista/internal/workerproto"
)

func (c *workerResultConsumer) modelObservation(result workerproto.Result) error {
	o := result.Observation
	if result.Depth != 0 || result.RunID == "" || result.Turn <= 0 || o == nil || !strings.HasPrefix(o.RequestID, result.RunID+"/") || len(o.RequestID) > 100 || o.ModelID == "" || len(o.ModelID) > 512 || len(o.ReasoningEffort) > 256 || len(o.Endpoint) != 64 || o.InputTokens < 0 || o.InputTokens > 1_000_000_000 || o.CacheReadTokens < 0 || o.CacheReadTokens > 1_000_000_000 || o.Purpose != "conversation" {
		return errors.New("invalid model observation")
	}
	if o.FirstTextMS != nil && (math.IsNaN(*o.FirstTextMS) || math.IsInf(*o.FirstTextMS, 0) || *o.FirstTextMS < 0) {
		return errors.New("invalid first text latency")
	}
	if o.Outcome != "succeeded" && o.Outcome != "failed" && o.Outcome != "cancelled" && o.Outcome != "no_text" {
		return errors.New("invalid observation outcome")
	}
	runID, err := c.store.RunningRunID(c.ctx, c.sessionID)
	if err != nil || runID != result.RunID {
		return errors.New("observation is not from the active execution")
	}
	prefs, err := c.store.Preferences(c.ctx)
	if err != nil {
		return err
	}
	err = c.store.RecordModelObservation(c.ctx, store.ModelObservation{
		RequestID: o.RequestID, UserID: prefs.UserID, ModelID: o.ModelID, ReasoningEffort: o.ReasoningEffort,
		Endpoint: o.Endpoint, InputTokens: o.InputTokens, FirstTextMS: o.FirstTextMS, CacheReadTokens: o.CacheReadTokens,
		Outcome: o.Outcome, Purpose: o.Purpose, ObservedAt: time.Now().UTC(),
	})
	return err
}
