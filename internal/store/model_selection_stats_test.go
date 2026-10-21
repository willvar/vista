package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestSelectionStatisticsMatchesRankingBucketsAndUserFeedback(t *testing.T) {
	s := selectionStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		observation(t, s, fmt.Sprintf("matching-%d", i), "user", "model", "max", "current", 100, millis(100+float64(i)))
		observation(t, s, fmt.Sprintf("other-%d", i), "other-user", "model", "max", "other", 100, millis(1000+float64(i)))
	}
	observation(t, s, "long-input", "user", "model", "max", "current", 60000, millis(2000))
	observation(t, s, "low-effort", "user", "model", "low", "current", 100, millis(1))
	if err := s.RecordModelObservation(ctx, ModelObservation{RequestID: "failed", ModelID: "model", ReasoningEffort: "max", Endpoint: "current", Outcome: "failed"}); err != nil {
		t.Fatal(err)
	}
	addSelectionReply(t, s, "session", "run", "reply", "model", "max")
	if _, err := s.SetReplyFeedback(ctx, "user", "session", "reply", stringValue("up")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetReplyFeedback(ctx, "other-user", "session", "reply", stringValue("down")); err != nil {
		t.Fatal(err)
	}
	rows, err := s.SelectionStatistics(ctx, "user", []SelectionCandidate{{ModelID: "model", ReasoningEffort: "max", Endpoint: "current"}, {ModelID: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	stats := rows[0]
	ranked, err := s.ModelSpeed(ctx, "model", "max", "current", 100)
	if err != nil {
		t.Fatal(err)
	}
	bucket := stats.Speed.Buckets[0]
	if bucket.Count != ranked.Count || *bucket.MedianMS != ranked.MedianMS || !bucket.EndpointMatched || *bucket.P90MS != 103.6 || bucket.Failures != 1 {
		t.Fatalf("ranking and displayed sample disagree: stats=%+v ranked=%+v", bucket, ranked)
	}
	if stats.Speed.Count != 6 || stats.Speed.Failures != 1 || stats.Quality.Count != 1 || stats.Quality.Up != 1 || stats.Quality.Down != 0 || stats.Quality.Score != WilsonScore(1, 0) {
		t.Fatalf("wrong aggregate or leaked user/configuration: %+v", stats)
	}
	if rows[1].ReasoningEffort != "default" || rows[1].Speed.Count != 0 || rows[1].Speed.MedianMS != nil || rows[1].Speed.P90MS != nil || rows[1].Quality.Count != 0 || !rows[1].Quality.LowConfidence {
		t.Fatalf("unknown model invented data: %+v", rows[1])
	}
}

func TestDeletingSessionPreservesSelectionStatisticsAndLearning(t *testing.T) {
	s := selectionStore(t)
	ctx := context.Background()
	addSelectionReply(t, s, "session", "run", "reply", "model", "max")
	observation(t, s, "request", "user", "model", "max", "endpoint", 100, millis(123))
	for user, vote := range map[string]string{"user": "up", "other-user": "down"} {
		if _, err := s.SetReplyFeedback(ctx, user, "session", "reply", stringValue(vote)); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishModelAttempt(ctx, user, "model", "max", "succeeded"); err != nil {
			t.Fatal(err)
		}
	}
	candidates := []SelectionCandidate{{ModelID: "model", ReasoningEffort: "max", Endpoint: "endpoint"}}
	readStats := func(user string) ([]ModelSelectionStatistics, SpeedStats, QualityStats) {
		t.Helper()
		stats, err := s.SelectionStatistics(ctx, user, candidates)
		if err != nil {
			t.Fatal(err)
		}
		speed, err := s.ModelSpeed(ctx, "model", "max", "endpoint", 100)
		if err != nil {
			t.Fatal(err)
		}
		quality, err := s.ModelQuality(ctx, user, "model", "max")
		if err != nil {
			t.Fatal(err)
		}
		return stats, speed, quality
	}
	before, speedBefore, qualityBefore := readStats("user")
	otherBefore, _, otherQualityBefore := readStats("other-user")
	var attemptsBefore []attemptRow
	if err := s.orm.Order("scope").Find(&attemptsBefore).Error; err != nil {
		t.Fatal(err)
	}
	messages, err := s.MessagesBefore(ctx, "session", 0, 100)
	if err != nil || len(messages) != 2 {
		t.Fatalf("reply fixture missing messages: %+v, %v", messages, err)
	}
	if err := s.Delete(ctx, "session"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "session"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted session remains: %v", err)
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("deleted session remains in manager: %+v, %v", list, err)
	}
	messages, err = s.MessagesBefore(ctx, "session", 0, 100)
	if err != nil || len(messages) != 0 {
		t.Fatalf("deleted messages remain: %+v, %v", messages, err)
	}
	history, err := s.HistoryIndex(ctx, "session", 1<<60, 10)
	if err != nil || len(history) != 0 {
		t.Fatalf("deleted history remains: %+v, %v", history, err)
	}
	after, speedAfter, qualityAfter := readStats("user")
	otherAfter, _, otherQualityAfter := readStats("other-user")
	if !reflect.DeepEqual(before, after) || speedBefore != speedAfter || qualityBefore != qualityAfter || !speedAfter.Known || !qualityAfter.Known {
		t.Fatalf("session deletion changed model learning: stats=%+v/%+v speed=%+v/%+v quality=%+v/%+v", before, after, speedBefore, speedAfter, qualityBefore, qualityAfter)
	}
	if !reflect.DeepEqual(otherBefore, otherAfter) || otherQualityBefore != otherQualityAfter || qualityAfter.Up != 1 || qualityAfter.Down != 0 || otherQualityAfter.Up != 0 || otherQualityAfter.Down != 1 {
		t.Fatalf("session deletion lost or mixed user feedback: user=%+v other=%+v", qualityAfter, otherQualityAfter)
	}
	var attemptsAfter []attemptRow
	if err := s.orm.Order("scope").Find(&attemptsAfter).Error; err != nil {
		t.Fatal(err)
	}
	if len(attemptsBefore) != 3 || !reflect.DeepEqual(attemptsBefore, attemptsAfter) {
		t.Fatalf("session deletion changed known attempts: before=%+v after=%+v", attemptsBefore, attemptsAfter)
	}
	for _, tier := range []string{"speed", "quality"} {
		choice, err := s.selectModel(ctx, "user", tier, candidates, 100, func(n int) int { return n - 1 })
		if err != nil || choice.Reason != "recommended" || choice.SampleCount != 1 {
			t.Fatalf("deleted session made %s model unknown: %+v, %v", tier, choice, err)
		}
	}
	_, _, strangerQuality := readStats("stranger")
	if strangerQuality.Known || strangerQuality.Count != 0 {
		t.Fatalf("feedback leaked to a new user: %+v", strangerQuality)
	}
	choice, err := s.selectModel(ctx, "stranger", "quality", candidates, 100, func(n int) int { return n - 1 })
	if err != nil || choice.Reason != "first_use" {
		t.Fatalf("quality learning was shared across users: %+v, %v", choice, err)
	}
	choice, err = s.selectModel(ctx, "stranger", "speed", candidates, 100, func(n int) int { return n - 1 })
	if err != nil || choice.Reason != "recommended" || choice.SampleCount != 1 {
		t.Fatalf("global speed learning was lost for another user: %+v, %v", choice, err)
	}
}

func TestResetSelectionStatisticsScopesKeepMessagesPreferencesAndPending(t *testing.T) {
	s := selectionStore(t)
	ctx := context.Background()
	prefs, err := s.PatchPreferences(ctx, PreferencesPatch{LastMode: stringValue("auto"), ManualModelKey: stringValue("manual-model")})
	if err != nil {
		t.Fatal(err)
	}
	observation(t, s, "request", prefs.UserID, "model", "max", "endpoint", 100, millis(123))
	addSelectionReply(t, s, "session", "run", "reply", "model", "max")
	for _, user := range []string{prefs.UserID, "other-user"} {
		if _, err := s.SetReplyFeedback(ctx, user, "session", "reply", stringValue("up")); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishModelAttempt(ctx, user, "model", "max", "succeeded"); err != nil {
			t.Fatal(err)
		}
	}
	// Keep a second model reserved by an in-flight task throughout both resets.
	candidates := []SelectionCandidate{{ModelID: "pending", ReasoningEffort: "max"}}
	if _, err := s.SelectModel(ctx, prefs.UserID, "speed", candidates, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectModel(ctx, prefs.UserID, "quality", candidates, 100); err != nil {
		t.Fatal(err)
	}
	var reservations []attemptRow
	if err := s.orm.Where("model_id = ?", "pending").Order("scope").Find(&reservations).Error; err != nil {
		t.Fatal(err)
	}
	if len(reservations) != 2 || reservations[0].PendingID == "" || reservations[1].PendingID == "" {
		t.Fatalf("missing pending attempts: %+v", reservations)
	}
	if err := s.NoteConnectionFailure(ctx, "model", "endpoint"); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetSelectionStatistics(ctx, prefs.UserID, "quality"); err != nil {
		t.Fatal(err)
	}
	quality, _ := s.ModelQuality(ctx, prefs.UserID, "model", "max")
	other, _ := s.ModelQuality(ctx, "other-user", "model", "max")
	speed, _ := s.ModelSpeed(ctx, "model", "max", "endpoint", 100)
	if quality.Count != 0 || other.Count != 1 || speed.Count != 1 {
		t.Fatalf("quality reset crossed scopes: own=%+v other=%+v speed=%+v", quality, other, speed)
	}
	if err := s.ResetSelectionStatistics(ctx, prefs.UserID, "speed"); err != nil {
		t.Fatal(err)
	}
	speed, _ = s.ModelSpeed(ctx, "model", "max", "endpoint", 100)
	cooldowns, _ := s.ConnectionCooldowns(ctx)
	if speed.Known || len(cooldowns) != 0 {
		t.Fatalf("speed reset retained rankings/penalties: %+v %+v", speed, cooldowns)
	}
	// Previously completed data cannot reappear when the worker retries delivery.
	observation(t, s, "request", prefs.UserID, "model", "max", "endpoint", 100, millis(123))
	speed, _ = s.ModelSpeed(ctx, "model", "max", "endpoint", 100)
	if speed.Known {
		t.Fatal("pre-reset delivery restored a removed observation")
	}
	for _, tier := range []string{"speed", "quality"} {
		choice, err := s.SelectModel(ctx, prefs.UserID, tier, []SelectionCandidate{{ModelID: "model", ReasoningEffort: "max"}}, 100)
		if err != nil || choice.Reason != "first_use" {
			t.Fatalf("reset did not restart %s learning: choice=%+v err=%v", tier, choice, err)
		}
	}
	var after []attemptRow
	if err := s.orm.Where("model_id = ?", "pending").Order("scope").Find(&after).Error; err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 {
		t.Fatal("reset removed an active reservation")
	}
	for i := range after {
		if after[i].PendingID != reservations[i].PendingID || !after[i].PendingAt.Equal(reservations[i].PendingAt) {
			t.Fatal("reset altered a reservation")
		}
	}
	var count int64
	if err := s.orm.Table("messages").Where("id = ?", "reply").Count(&count).Error; err != nil || count != 1 {
		t.Fatal("reset removed the original reply")
	}
	current, _ := s.Preferences(ctx)
	if current.UserID != prefs.UserID || current.LastMode != "auto" || current.ManualModelKey != "manual-model" {
		t.Fatal("reset changed identity or selection preferences")
	}
	var otherAttempt attemptRow
	if err := s.orm.Where("scope = ? AND model_id = ?", "quality:other-user", "model").Take(&otherAttempt).Error; err != nil || otherAttempt.Attempts == 0 {
		t.Fatal("reset changed another user's quality attempts")
	}
	if err := s.ResetSelectionStatistics(ctx, prefs.UserID, "unknown"); err == nil {
		t.Fatal("invalid reset scope accepted")
	}
}
