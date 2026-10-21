package store

import (
	"context"
	"errors"
	"sort"
	"time"

	"gorm.io/gorm"
)

type SelectionSpeedSummary struct {
	Count         int      `json:"count"`
	Failures      int      `json:"failures"`
	MedianMS      *float64 `json:"medianMs"`
	P90MS         *float64 `json:"p90Ms"`
	LowConfidence bool     `json:"lowConfidence"`
}

type SelectionSpeedBucket struct {
	SelectionSpeedSummary
	InputBucket     int  `json:"inputBucket"`
	EndpointMatched bool `json:"endpointMatched"`
}

type SelectionSpeedStatistics struct {
	SelectionSpeedSummary
	Buckets []SelectionSpeedBucket `json:"buckets"`
}

type SelectionQualityStatistics struct {
	Up            int     `json:"up"`
	Down          int     `json:"down"`
	Count         int     `json:"count"`
	Score         float64 `json:"score"`
	LowConfidence bool    `json:"lowConfidence"`
}

type ModelSelectionStatistics struct {
	ModelID         string                     `json:"modelId"`
	ReasoningEffort string                     `json:"reasoningEffort"`
	Speed           SelectionSpeedStatistics   `json:"speed"`
	Quality         SelectionQualityStatistics `json:"quality"`
}

func speedSummary(values []float64, failures int) SelectionSpeedSummary {
	result := SelectionSpeedSummary{Count: len(values), Failures: failures, LowConfidence: len(values) < 5}
	if len(values) > 0 {
		sort.Float64s(values)
		median, p90 := percentile(values, .5), percentile(values, .9)
		result.MedianMS, result.P90MS = &median, &p90
	}
	return result
}

// SelectionStatistics reads the same recent samples, input buckets, endpoint
// preference and feedback window as selection. The overall latency is only a
// display summary; selection compares models within an input bucket.
func (s *Store) SelectionStatistics(ctx context.Context, userID string, candidates []SelectionCandidate) ([]ModelSelectionStatistics, error) {
	result := make([]ModelSelectionStatistics, 0, len(candidates))
	if len(candidates) == 0 {
		return result, nil
	}
	ids := make([]string, 0, len(candidates))
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if !seen[candidate.ModelID] {
			ids = append(ids, candidate.ModelID)
			seen[candidate.ModelID] = true
		}
	}
	err := s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var observations []ModelObservation
		if err := tx.Where("model_id IN ? AND purpose = ? AND observed_at >= ?", ids, "conversation", time.Now().UTC().Add(-30*24*time.Hour)).Order("observed_at DESC, request_id DESC").Find(&observations).Error; err != nil {
			return err
		}
		type sampleBucket struct {
			values, matched                        []float64
			failures, matchedFailures, noTextCount int
		}
		buckets := map[string]*[4]sampleBucket{}
		endpoints := map[string]string{}
		for _, candidate := range candidates {
			effort := candidate.ReasoningEffort
			if effort == "" {
				effort = "default"
			}
			key := candidate.ModelID + "\x00" + effort
			if buckets[key] == nil {
				buckets[key] = &[4]sampleBucket{}
				endpoints[key] = candidate.Endpoint
			}
		}
		for _, row := range observations {
			key := row.ModelID + "\x00" + row.ReasoningEffort
			modelBuckets := buckets[key]
			if modelBuckets == nil || row.InputBucket < 0 || row.InputBucket >= len(modelBuckets) {
				continue
			}
			bucket := &modelBuckets[row.InputBucket]
			// Respect the selection window even if old data has not been pruned.
			if row.FirstTextMS != nil {
				if len(bucket.values) >= 200 {
					continue
				}
				bucket.values = append(bucket.values, *row.FirstTextMS)
				if endpoints[key] != "" && row.Endpoint == endpoints[key] {
					bucket.matched = append(bucket.matched, *row.FirstTextMS)
				}
			} else {
				if bucket.noTextCount >= 200 {
					continue
				}
				bucket.noTextCount++
			}
			if row.Outcome == "failed" {
				bucket.failures++
				if endpoints[key] != "" && row.Endpoint == endpoints[key] {
					bucket.matchedFailures++
				}
			}
		}
		var feedback []feedbackRow
		if err := tx.Where("user_id = ? AND model_id IN ?", userID, ids).Order("reply_at DESC, message_id DESC").Find(&feedback).Error; err != nil {
			return err
		}
		quality := map[string]SelectionQualityStatistics{}
		for _, row := range feedback {
			key := row.ModelID + "\x00" + row.ReasoningEffort
			stats := quality[key]
			if buckets[key] == nil || stats.Count >= 200 {
				continue
			}
			stats.Count++
			if row.Value == "up" {
				stats.Up++
			} else {
				stats.Down++
			}
			quality[key] = stats
		}
		seen := map[string]bool{}
		for _, candidate := range candidates {
			effort := candidate.ReasoningEffort
			if effort == "" {
				effort = "default"
			}
			key := candidate.ModelID + "\x00" + effort
			if seen[key] {
				continue
			}
			seen[key] = true
			item := ModelSelectionStatistics{ModelID: candidate.ModelID, ReasoningEffort: effort, Quality: quality[key]}
			item.Quality.Score = WilsonScore(item.Quality.Up, item.Quality.Down)
			item.Quality.LowConfidence = item.Quality.Count < 3
			values, failures := []float64{}, 0
			for index, bucket := range buckets[key] {
				matched := len(bucket.matched) >= 5
				if matched {
					bucket.values, bucket.failures = bucket.matched, bucket.matchedFailures
				}
				values = append(values, bucket.values...)
				failures += bucket.failures
				item.Speed.Buckets = append(item.Speed.Buckets, SelectionSpeedBucket{SelectionSpeedSummary: speedSummary(bucket.values, bucket.failures), InputBucket: index, EndpointMatched: matched})
			}
			item.Speed.SelectionSpeedSummary = speedSummary(values, failures)
			result = append(result, item)
		}
		return nil
	})
	return result, err
}

func (s *Store) ResetSelectionStatistics(ctx context.Context, userID, scope string) error {
	if scope != "speed" && scope != "quality" && scope != "all" {
		return errors.New("invalid statistics scope")
	}
	if userID == "" && scope != "speed" {
		return errors.New("missing statistics identity")
	}
	return s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scopes := []string{}
		if scope == "speed" || scope == "all" {
			// Receipts remain: a replayed pre-reset request must not restore data.
			if err := tx.Where("purpose = ?", "conversation").Delete(&ModelObservation{}).Error; err != nil {
				return err
			}
			if err := tx.Where("1 = 1").Delete(&modelConnectionHealth{}).Error; err != nil {
				return err
			}
			scopes = append(scopes, "speed")
		}
		if scope == "quality" || scope == "all" {
			if err := tx.Where("user_id = ?", userID).Delete(&feedbackRow{}).Error; err != nil {
				return err
			}
			scopes = append(scopes, "quality:"+userID)
		}
		// Reservations belong to running tasks. Retain their IDs and timestamps.
		if err := tx.Where("scope IN ? AND pending_id = ?", scopes, "").Delete(&attemptRow{}).Error; err != nil {
			return err
		}
		return tx.Model(&attemptRow{}).Where("scope IN ? AND pending_id <> ?", scopes, "").Updates(map[string]any{"attempts": 0, "explorations": 0, "last_attempt": time.Time{}, "outcome": ""}).Error
	})
}
