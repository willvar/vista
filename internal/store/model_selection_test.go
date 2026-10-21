package store

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func selectionStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "selection.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func stringValue(value string) *string { return &value }

func TestSelectionPreferencesPersistIdentityAndIndependentMemories(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "selection.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := s.Preferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := true
	if _, err := s.PatchPreferences(ctx, PreferencesPatch{LastMode: stringValue("auto"), LastTier: stringValue("quality"), ManualModelKey: stringValue("manual-model"), ReasoningByModelID: map[string]string{"model-a": "low"}, FeedbackInvitationSeen: &seen}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PatchPreferences(ctx, PreferencesPatch{ReasoningByModelID: map[string]string{"model-b": "max"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	prefs, err := s.Preferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if prefs.UserID != initial.UserID || prefs.LastMode != "auto" || prefs.LastTier != "quality" || prefs.ManualModelKey != "manual-model" || prefs.ReasoningByModelID["model-a"] != "low" || prefs.ReasoningByModelID["model-b"] != "max" || !prefs.FeedbackInvitationSeen {
		t.Fatalf("preferences lost: %+v", prefs)
	}
	if _, err := s.Create(ctx, "session", "title"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PatchSessionSelection(ctx, "session", SessionSelectionPatch{SelectionMode: stringValue("auto"), SelectionTier: stringValue("quality"), ManualModelKey: stringValue("manual-model"), ManualReasoningEffort: stringValue("low")}); err != nil {
		t.Fatal(err)
	}
	if err := s.BindSessionSelection(ctx, "session", "automatic-model", "max"); err != nil {
		t.Fatal(err)
	}
	item, err := s.Get(ctx, "session")
	if err != nil {
		t.Fatal(err)
	}
	if item.ModelKey != "automatic-model" || item.ManualModelKey != "manual-model" || item.ManualReasoningEffort != "low" {
		t.Fatalf("automatic selection changed manual memory: %+v", item)
	}
	if _, err := s.PatchSessionSelection(ctx, "session", SessionSelectionPatch{SelectionTier: stringValue("speed")}); err != nil {
		t.Fatal(err)
	}
	item, _ = s.Get(ctx, "session")
	if item.SelectionReasoningEffort != "" {
		t.Fatal("changing tier retained the old automatic binding")
	}
}

func observation(t *testing.T, s *Store, id, user, model, effort, endpoint string, tokens int64, ms *float64) {
	t.Helper()
	if err := s.RecordModelObservation(context.Background(), ModelObservation{RequestID: id, UserID: user, ModelID: model, ReasoningEffort: effort, Endpoint: endpoint, InputTokens: tokens, FirstTextMS: ms, Outcome: "completed", Purpose: "conversation"}); err != nil {
		t.Fatal(err)
	}
}
func millis(value float64) *float64 { return &value }

func TestSelectionSpeedSharedEndpointEffortBucketAndOutcomes(t *testing.T) {
	s := selectionStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		observation(t, s, fmt.Sprintf("a%d", i), "user-a", "same-model", "max", "endpoint-a", 1000, millis(100+float64(i)))
		observation(t, s, fmt.Sprintf("b%d", i), "user-b", "same-model", "max", "endpoint-b", 1000, millis(1000+float64(i)))
	}
	observation(t, s, "other-effort", "user-a", "same-model", "low", "endpoint-a", 1000, millis(1))
	observation(t, s, "large-input", "user-a", "same-model", "max", "endpoint-a", 60000, millis(2000))
	observation(t, s, "no-body", "user-a", "same-model", "max", "endpoint-a", 1000, nil)
	if err := s.RecordModelObservation(ctx, ModelObservation{RequestID: "title", ModelID: "same-model", ReasoningEffort: "max", InputTokens: 1000, FirstTextMS: millis(1), Purpose: "title"}); err != nil {
		t.Fatal(err)
	}
	matched, err := s.ModelSpeed(ctx, "same-model", "max", "endpoint-a", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if matched.Count != 5 || matched.MedianMS != 102 || matched.IQRMS != 2 || !matched.EndpointMatched {
		t.Fatalf("endpoint data incorrect: %+v", matched)
	}
	shared, err := s.ModelSpeed(ctx, "same-model", "max", "new-endpoint", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if shared.Count != 10 || shared.MedianMS != 552 || shared.EndpointMatched {
		t.Fatalf("shared data incorrect: %+v", shared)
	}
	missing, _ := s.ModelSpeed(ctx, "same-model", "max", "endpoint-a", 7000)
	if missing.Known {
		t.Fatalf("missing input bucket borrowed short-input data: %+v", missing)
	}
	observation(t, s, "a0", "user-a", "same-model", "max", "endpoint-a", 1000, millis(0))
	matched, _ = s.ModelSpeed(ctx, "same-model", "max", "endpoint-a", 1000)
	if matched.Count != 5 || matched.MedianMS != 102 {
		t.Fatalf("duplicate request changed measurements: %+v", matched)
	}
}

func TestSelectionSpeedWindowAndPrunedRequestDedup(t *testing.T) {
	s := selectionStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < 205; i++ {
		ms := float64(i + 1)
		err := s.RecordModelObservation(ctx, ModelObservation{RequestID: fmt.Sprintf("req-%03d", i), ModelID: "window-model", ReasoningEffort: "max", InputTokens: 100, FirstTextMS: &ms, Purpose: "conversation", ObservedAt: now.Add(time.Duration(i-205) * time.Second)})
		if err != nil {
			t.Fatal(err)
		}
	}
	stats, err := s.ModelSpeed(ctx, "window-model", "max", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Count != 200 || stats.MedianMS != 105.5 {
		t.Fatalf("rolling cap incorrect: %+v", stats)
	}
	// Replaying a request evicted by the window still must not count again.
	observation(t, s, "req-000", "", "window-model", "max", "", 100, millis(0))
	after, _ := s.ModelSpeed(ctx, "window-model", "max", "", 100)
	if after.MedianMS != stats.MedianMS {
		t.Fatal("evicted request was counted twice")
	}
	old := millis(.1)
	if err := s.RecordModelObservation(ctx, ModelObservation{RequestID: "expired", ModelID: "window-model", ReasoningEffort: "max", InputTokens: 100, FirstTextMS: old, Purpose: "conversation", ObservedAt: now.Add(-31 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	after, _ = s.ModelSpeed(ctx, "window-model", "max", "", 100)
	if after.Count != 200 || after.MedianMS != stats.MedianMS {
		t.Fatalf("expired observation entered ranking: %+v", after)
	}
}

func TestSelectionColdStartReservesDistinctIDsAndFailureCountsAsTried(t *testing.T) {
	s := selectionStore(t)
	ctx := context.Background()
	candidates := []SelectionCandidate{{ModelID: "one", ModelKey: "one-connection", ReasoningEffort: "max"}, {ModelID: "one", ModelKey: "duplicate-connection", ReasoningEffort: "max"}, {ModelID: "two", ModelKey: "two-connection", ReasoningEffort: "max"}}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var results []SelectionDecision
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			choice, err := s.selectModel(ctx, "user", "speed", candidates, 100, func(int) int { return 0 })
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			results = append(results, choice)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(results) != 2 || results[0].ModelID == results[1].ModelID {
		t.Fatalf("parallel cold start repeated a model ID: %+v", results)
	}
	for _, choice := range results {
		if err := s.FinishModelAttempt(ctx, "user", choice.ModelID, "max", "failed"); err != nil {
			t.Fatal(err)
		}
	}
	choice, err := s.selectModel(ctx, "user", "speed", candidates, 100, func(int) int { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	if choice.Reason == "first_use" {
		t.Fatal("failed model became untried again")
	}
	// Speed attempts are shared; quality attempts still belong to the user.
	choice, err = s.selectModel(ctx, "another-user", "quality", candidates, 100, func(int) int { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	if choice.Reason != "first_use" {
		t.Fatal("another user's quality cold start was skipped")
	}
}

func TestSelectionSpeedNearBestAndContextWithNoDataRotates(t *testing.T) {
	s := selectionStore(t)
	ctx := context.Background()
	candidates := []SelectionCandidate{{ModelID: "fast", ReasoningEffort: "max"}, {ModelID: "near", ReasoningEffort: "max"}, {ModelID: "slow", ReasoningEffort: "max"}}
	for i, item := range candidates {
		for j := 0; j < 5; j++ {
			observation(t, s, fmt.Sprintf("speed-%d-%d", i, j), "user", item.ModelID, "max", "", 100, millis([]float64{100, 120, 500}[i]))
		}
	}
	choice, err := s.selectModel(ctx, "user", "speed", candidates, 100, func(n int) int { return n - 1 })
	if err != nil {
		t.Fatal(err)
	}
	if choice.ModelID != "near" || choice.Reason != "recommended" {
		t.Fatalf("near-best pool incorrect: %+v", choice)
	}
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		choice, err := s.selectModel(ctx, "user", "speed", candidates, 50000, func(int) int { return 0 })
		if err != nil {
			t.Fatal(err)
		}
		if choice.Reason != "no_data" {
			t.Fatalf("short-input result used for larger context: %+v", choice)
		}
		if seen[choice.ModelID] {
			t.Fatal("no-data rotation repeated a model before completing a round")
		}
		seen[choice.ModelID] = true
	}
}

func addSelectionReply(t *testing.T, s *Store, sessionID, runID, replyID, modelID, effort string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Get(ctx, sessionID); err != nil {
		if _, err := s.Create(ctx, sessionID, "feedback"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.BeginRun(ctx, sessionID, runID, runID+"-input", "Hi"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordModelRequest(ctx, sessionID, runID, "openai-responses", modelID); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRunSelection(ctx, runID, modelID, effort, "endpoint"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRun(ctx, sessionID, runID, replyID, "succeeded", "Hello"); err != nil {
		t.Fatal(err)
	}
}

func TestSelectionFeedbackUsesActualReplyAndChangesWithoutExtraVotes(t *testing.T) {
	s := selectionStore(t)
	ctx := context.Background()
	addSelectionReply(t, s, "session", "run", "reply", "actual-model", "max")
	if _, err := s.SetReplyFeedback(ctx, "user-a", "session", "reply", stringValue("up")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetReplyFeedback(ctx, "user-a", "session", "reply", stringValue("up")); err != nil {
		t.Fatal(err)
	}
	stats, _ := s.ModelQuality(ctx, "user-a", "actual-model", "max")
	if stats.Count != 1 || stats.Up != 1 || stats.Score <= 0 {
		t.Fatalf("repeated vote counted twice: %+v", stats)
	}
	if _, err := s.SetReplyFeedback(ctx, "user-a", "session", "reply", stringValue("down")); err != nil {
		t.Fatal(err)
	}
	stats, _ = s.ModelQuality(ctx, "user-a", "actual-model", "max")
	if stats.Count != 1 || stats.Down != 1 || stats.Up != 0 || stats.Score != 0 {
		t.Fatalf("changing vote failed: %+v", stats)
	}
	other, _ := s.ModelQuality(ctx, "user-b", "actual-model", "max")
	if other.Known {
		t.Fatal("quality feedback leaked to another user")
	}
	low, _ := s.ModelQuality(ctx, "user-a", "actual-model", "low")
	if low.Known {
		t.Fatal("feedback mixed reasoning configurations")
	}
	if _, err := s.SetReplyFeedback(ctx, "user-a", "session", "reply", nil); err != nil {
		t.Fatal(err)
	}
	stats, _ = s.ModelQuality(ctx, "user-a", "actual-model", "max")
	if stats.Known || stats.Count != 0 {
		t.Fatalf("withdrawn vote remains: %+v", stats)
	}
	if _, err := s.SetReplyFeedback(ctx, "user-a", "session", "run-input", stringValue("up")); err != ErrFeedbackTarget {
		t.Fatalf("user message accepted feedback: %v", err)
	}
}

func TestSelectionQualityUnknownSeparateAndNoFeedbackRotation(t *testing.T) {
	s := selectionStore(t)
	ctx := context.Background()
	candidates := []SelectionCandidate{{ModelID: "no-feedback", ReasoningEffort: "max"}, {ModelID: "good", ReasoningEffort: "max"}, {ModelID: "bad", ReasoningEffort: "max"}}
	for _, item := range candidates {
		if err := s.FinishModelAttempt(ctx, "user", item.ModelID, "max", "completed"); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		choice, err := s.selectModel(ctx, "user", "quality", candidates, 100, func(int) int { return 0 })
		if err != nil {
			t.Fatal(err)
		}
		if seen[choice.ModelID] {
			t.Fatal("no-feedback rotation repeated before round completed")
		}
		seen[choice.ModelID] = true
	}
	addSelectionReply(t, s, "session", "good-run", "good-reply", "good", "max")
	addSelectionReply(t, s, "session", "bad-run", "bad-reply", "bad", "max")
	if _, err := s.SetReplyFeedback(ctx, "user", "session", "good-reply", stringValue("up")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetReplyFeedback(ctx, "user", "session", "bad-reply", stringValue("down")); err != nil {
		t.Fatal(err)
	}
	choice, err := s.selectModel(ctx, "user", "quality", candidates, 100, func(n int) int { return n - 1 })
	if err != nil {
		t.Fatal(err)
	}
	if choice.ModelID != "good" {
		t.Fatalf("unrated/all-disliked entered exploitation: %+v", choice)
	}
	if WilsonScore(1, 0) >= WilsonScore(10, 0) {
		t.Fatal("sample confidence ignored")
	}
	if math.Abs(WilsonScore(1, 0)-.3784474964) > .00001 {
		t.Fatalf("Wilson score = %v", WilsonScore(1, 0))
	}
}

func TestSelectionFeedbackWindowUsesReplyOrderNotClickOrder(t *testing.T) {
	s := selectionStore(t)
	ctx := context.Background()
	rows := make([]feedbackRow, 201)
	for i := range rows {
		rows[i] = feedbackRow{UserID: "user", MessageID: fmt.Sprintf("message-%03d", i), ModelID: "model", ReasoningEffort: "max", Value: "up", ReplyAt: fmt.Sprintf("2026-10-03T%03d", i)}
	}
	rows[0].Value = "down"
	if err := s.orm.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	stats, _ := s.ModelQuality(ctx, "user", "model", "max")
	if stats.Count != 200 || stats.Down != 0 {
		t.Fatalf("feedback window incorrect: %+v", stats)
	}
	if err := s.orm.Model(&feedbackRow{}).Where("user_id = ? AND message_id = ?", "user", "message-000").Update("value", "up").Error; err != nil {
		t.Fatal(err)
	}
	stats, _ = s.ModelQuality(ctx, "user", "model", "max")
	if stats.Count != 200 || stats.Up != 200 {
		t.Fatal("updating old feedback changed reply window")
	}
}

func TestSelectionEstimateIncludesHistoryNonASCIIAndImages(t *testing.T) {
	s := selectionStore(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, "session", "estimate"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRunWithImages(ctx, "session", "run", "message", "你好 world", nil); err != nil {
		t.Fatal(err)
	}
	base, err := s.SelectionInputEstimate(ctx, "session", SelectionTextTokens("Hi"), false)
	if err != nil {
		t.Fatal(err)
	}
	if base < 2048+1+SelectionTextTokens("你好 world") {
		t.Fatalf("history omitted: %d", base)
	}
	if SelectionTextTokens("中文") != 4 || SelectionTextTokens("abcdef") != 2 {
		t.Fatal("text token estimate differs from stated convention")
	}
	// No payload inspection is needed to account for historical images.
	image := fixtureImage(t)
	unlock, err := s.lockImages(ctx, []Image{image})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		unlock()
		t.Fatal(err)
	}
	err = s.writeImages(ctx, tx, "session", "message_resources", "message_id", "message", []Image{image})
	if err == nil {
		err = tx.Commit()
	}
	tx.Rollback()
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	withImage, _ := s.SelectionInputEstimate(ctx, "session", 1, false)
	if withImage != base+4096 {
		t.Fatalf("history image omitted: base=%d with=%d", base, withImage)
	}
}

func TestSelectionColdPendingRecoveryDoesNotStayUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selection.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	candidates := []SelectionCandidate{{ModelID: "model", ReasoningEffort: "max"}}
	if _, err := s.selectModel(ctx, "user", "speed", candidates, 100, func(int) int { return 0 }); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	choice, err := s.selectModel(ctx, "user", "speed", candidates, 100, func(int) int { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	if choice.Reason == "first_use" {
		t.Fatal("server restart retained cold-start pending indefinitely")
	}
}

func TestSelectionFailureCooldownKeepsOtherConnectionsUsable(t *testing.T) {
	s := selectionStore(t)
	ctx := context.Background()
	if err := s.RecordModelObservation(ctx, ModelObservation{RequestID: "failed-request", UserID: "user", ModelID: "model", ReasoningEffort: "max", Endpoint: "bad", InputTokens: 100, Outcome: "failed", Purpose: "conversation"}); err != nil {
		t.Fatal(err)
	}
	candidates := []SelectionCandidate{{ModelID: "model", ModelKey: "bad", ReasoningEffort: "max", Endpoint: "bad"}, {ModelID: "model", ModelKey: "good", ReasoningEffort: "max", Endpoint: "good"}}
	choice, err := s.selectModel(ctx, "user", "speed", candidates, 100, func(int) int { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	if choice.ModelKey != "good" {
		t.Fatalf("failure blocked good connection or reused bad one: %+v", choice)
	}
	if _, err := s.selectModel(ctx, "user", "speed", candidates[:1], 100, func(int) int { return 0 }); err == nil {
		t.Fatal("known failing endpoint entered selection during cooldown")
	}
	if err := s.RecordModelObservation(ctx, ModelObservation{RequestID: "recovered-request", UserID: "user", ModelID: "model", ReasoningEffort: "max", Endpoint: "bad", InputTokens: 100, Outcome: "completed", FirstTextMS: millis(200), Purpose: "conversation"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.selectModel(ctx, "user", "speed", candidates[:1], 100, func(int) int { return 0 }); err != nil {
		t.Fatalf("recovered endpoint not restored: %v", err)
	}
}
