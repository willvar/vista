package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"vista/internal/workerproto"
)

func TestActivityMinutesDeduplicateClientsAndRejectInvalidBatch(t *testing.T) {
	s := revertStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	setStatisticsActivitySince(t, s, now.Add(-2*time.Minute))
	minute := now.Unix() / 60
	for index, batch := range [][]int64{{minute, minute, minute - 1}, {minute, minute - 1}, {minute - 2}} {
		changed, err := s.RecordActivity(ctx, batch, now)
		if err != nil || changed != (index != 1) {
			t.Fatalf("batch %d: %t %v", index, changed, err)
		}
	}
	for _, batch := range [][]int64{nil, {minute - 3, minute - 1441}, {minute - 3, minute + 1}, make([]int64, 121)} {
		if _, err := s.RecordActivity(ctx, batch, now); err == nil {
			t.Fatalf("accepted invalid batch: %v", batch)
		}
	}
	stats, err := s.Statistics(ctx, 7, "UTC", now)
	if err != nil || stats.ActiveMinutes != 3 || stats.ActiveDays != 1 || stats.Hours[1][12] != 1 || stats.Hours[1][11] != 2 {
		t.Fatalf("unexpected activity: %+v %v", stats, err)
	}
}

func TestStatisticsAlignActivityAndUsageAcrossFractionalOffsetMidnight(t *testing.T) {
	s := revertStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 20, 0, 0, 0, time.UTC)
	before := time.Date(2026, 11, 3, 18, 14, 0, 0, time.UTC)
	after := before.Add(time.Minute)
	setStatisticsActivitySince(t, s, before)
	if _, err := s.RecordActivity(ctx, []int64{before.Unix() / 60, after.Unix() / 60}, now); err != nil {
		t.Fatal(err)
	}
	total := int64(125)
	for index, at := range []time.Time{before, after} {
		id := []string{"one", "two"}[index]
		if changed, err := s.RecordTokenUsage(ctx, id, &total, at); err != nil || !changed {
			t.Fatal(changed, err)
		}
		if changed, err := s.RecordTokenUsage(ctx, id, &total, at); err != nil || changed {
			t.Fatal("duplicate changed consumption", changed, err)
		}
	}
	if _, err := s.RecordTokenUsage(ctx, "unknown", nil, after); err != nil {
		t.Fatal(err)
	}
	stats, err := s.Statistics(ctx, 7, "Asia/Kathmandu", now)
	if err != nil {
		t.Fatal(err)
	}
	previous, today := stats.Days[5], stats.Days[6]
	if previous.Date != "2026-11-03" || today.Date != "2026-11-04" || previous.Tokens != total || today.Tokens != total || previous.ActiveMinutes != 1 || today.ActiveMinutes != 1 || today.Unreported != 1 || stats.Tokens != 250 || stats.Requests != 3 || stats.Unreported != 1 {
		t.Fatalf("bad local-day aggregation: %+v", stats)
	}
	if stats.Hours[1][23] != 1 || stats.Hours[2][0] != 1 {
		t.Fatalf("incorrect local hours: %+v", stats.Hours)
	}
	if err := s.Delete(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	afterDelete, err := s.Statistics(ctx, 7, "Asia/Kathmandu", now)
	if err != nil || afterDelete.Tokens != stats.Tokens || afterDelete.ActiveMinutes != stats.ActiveMinutes {
		t.Fatal("deleting history removed consumption", afterDelete, err)
	}
}

func TestStatisticsDSTRepeatedHourCountsActualDistinctMinutes(t *testing.T) {
	s := revertStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	first := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC).Unix() / 60
	setStatisticsActivitySince(t, s, time.Unix(first*60, 0))
	second := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC).Unix() / 60
	if _, err := s.RecordActivity(ctx, []int64{first, second}, now); err != nil {
		t.Fatal(err)
	}
	stats, err := s.Statistics(ctx, 7, "America/New_York", now)
	if err != nil || stats.ActiveMinutes != 2 || stats.Hours[6][1] != 2 || stats.Days[6].ActiveMinutes != 2 {
		t.Fatal(stats, err)
	}
	for _, tc := range []struct {
		days int
		zone string
	}{{8, "UTC"}, {7, "not/a-zone"}} {
		if _, err := s.Statistics(ctx, tc.days, tc.zone, now); err == nil {
			t.Fatal("invalid query accepted", tc)
		}
	}
}

func setStatisticsActivitySince(t *testing.T, s *Store, since time.Time) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE statistics_meta SET value=? WHERE key='activity_since'`, since.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
}

func TestStatisticsHeatmapAveragesObservedSlotsIncludingInactiveOccurrences(t *testing.T) {
	s := revertStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	setStatisticsActivitySince(t, s, now.AddDate(0, 0, -7).Add(-90*time.Minute))
	for _, entry := range []struct {
		at    time.Time
		count int
	}{
		{time.Date(2026, 10, 27, 10, 40, 0, 0, time.UTC), 1},
		{time.Date(2026, 11, 3, 9, 10, 0, 0, time.UTC), 5},
		{time.Date(2026, 10, 27, 13, 10, 0, 0, time.UTC), 3},
	} {
		for i := range entry.count {
			if _, err := s.db.Exec(`INSERT INTO activity_minutes(minute) VALUES(?)`, entry.at.Unix()/60+int64(i)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, days := range []int{30, 90} {
		stats, err := s.Statistics(ctx, days, "UTC", now)
		if err != nil || stats.ActiveMinutes != 9 || stats.Hours[1][10] != .5 || stats.Hours[1][9] != 5 || stats.Hours[1][13] != 3 {
			t.Fatal("average omitted quiet occurrences or included unobserved hours", days, stats, err)
		}
	}
	stats, err := s.Statistics(ctx, 7, "UTC", now)
	if err != nil || stats.ActiveMinutes != 5 || stats.Hours[1][10] != 0 || stats.Hours[1][9] != 5 || stats.Hours[1][13] != 0 {
		t.Fatal("average included occurrences outside the selected period", stats, err)
	}
}

func TestStatisticsHeatmapAveragesLocalSlotsAcrossDST(t *testing.T) {
	for _, scenario := range []struct {
		name, zone string
		since, now time.Time
		minutes    []time.Time
		hour       int
		want       float64
	}{
		{
			name: "skipped hour", zone: "America/New_York",
			since: time.Date(2026, 3, 1, 5, 0, 0, 0, time.UTC), now: time.Date(2026, 3, 8, 16, 0, 0, 0, time.UTC),
			minutes: []time.Time{time.Date(2026, 3, 1, 7, 30, 0, 0, time.UTC)}, hour: 2, want: 1,
		},
		{
			name: "repeated hour", zone: "America/New_York",
			since: time.Date(2026, 10, 25, 4, 0, 0, 0, time.UTC), now: time.Date(2026, 11, 1, 17, 0, 0, 0, time.UTC),
			minutes: []time.Time{time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC), time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)}, hour: 1, want: 1,
		},
		{
			name: "half-hour shift", zone: "Australia/Lord_Howe",
			since: time.Date(2026, 9, 26, 13, 30, 0, 0, time.UTC), now: time.Date(2026, 10, 4, 1, 0, 0, 0, time.UTC),
			minutes: []time.Time{time.Date(2026, 10, 3, 15, 45, 0, 0, time.UTC)}, hour: 2, want: .5,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			s := revertStore(t)
			setStatisticsActivitySince(t, s, scenario.since)
			for _, at := range scenario.minutes {
				if _, err := s.db.Exec(`INSERT INTO activity_minutes(minute) VALUES(?)`, at.Unix()/60); err != nil {
					t.Fatal(err)
				}
			}
			stats, err := s.Statistics(context.Background(), 30, scenario.zone, scenario.now)
			if err != nil || stats.Hours[6][scenario.hour] != scenario.want || stats.ActiveMinutes != int64(len(scenario.minutes)) {
				t.Fatal("incorrect local-slot average or changed daily totals", stats, err)
			}
		})
	}
}

func TestStatisticsLegacyBackfillRunsOnceAndNeverDoubleCountsNewUsage(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vista.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	if _, err := s.Create(ctx, "session", "legacy"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, "session", "run", "message", "hi"); err != nil {
		t.Fatal(err)
	}
	for index, usage := range []*workerproto.ModelUsage{{InputTokens: 100, OutputTokens: 25, CacheReadTokens: 50, ReasoningTokens: 10}, nil} {
		_, err := s.AppendRunEvent(ctx, "session", "run", "model_result", workerproto.Result{RunID: "run", Turn: int32(index + 1), ModelResult: &workerproto.ModelResult{Usage: usage}})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Simulate a pre-statistics database, then reopen as an upgraded application.
	if _, err := s.db.Exec(`DELETE FROM statistics_meta WHERE key='usage_since'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	total := int64(200)
	if _, err := s.RecordTokenUsage(ctx, "new-request", &total, time.Now()); err != nil {
		t.Fatal(err)
	}
	// A normal result saved for the new request must not be imported again.
	if _, err := s.AppendRunEvent(ctx, "session", "run", "model_result", workerproto.Result{RunID: "run", Turn: 3, ModelResult: &workerproto.ModelResult{Usage: &workerproto.ModelUsage{InputTokens: 150, OutputTokens: 50}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := s.Statistics(ctx, 7, "UTC", time.Now())
	if err != nil || stats.Tokens != 325 || stats.Requests != 3 || stats.Unreported != 1 {
		t.Fatal("backfill repeated, omitted usage, or added subsets twice", stats, err)
	}
}

func TestStatisticsRetentionMatchesLongestView(t *testing.T) {
	for _, trigger := range []string{"activity", "usage", "initialization"} {
		t.Run(trigger, func(t *testing.T) {
			s := revertStore(t)
			ctx := context.Background()
			now := time.Now().UTC()
			expired := now.AddDate(0, 0, -91).Unix() / 60
			retained := now.AddDate(0, 0, -89).Unix() / 60
			for _, minute := range []int64{expired, retained} {
				if _, err := s.db.Exec(`INSERT INTO activity_minutes(minute) VALUES(?)`, minute); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec(`INSERT INTO token_usage(id,minute,tokens) VALUES(?,?,10)`, time.Unix(minute*60, 0).Format(time.RFC3339), minute); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			switch trigger {
			case "activity":
				_, err = s.RecordActivity(ctx, []int64{now.Unix() / 60}, now)
			case "usage":
				total := int64(10)
				_, err = s.RecordTokenUsage(ctx, "current", &total, now)
			default:
				err = s.initStatistics()
			}
			if err != nil {
				t.Fatal(err)
			}
			stats, err := s.Statistics(ctx, 90, "UTC", now)
			if err != nil || stats.RetentionDays != 90 || len(stats.Days) != 90 || stats.Days[0].ActiveMinutes != 1 || stats.Days[0].Tokens != 10 {
				t.Fatal("longest view lost retained data or disagrees with retention", stats, err)
			}
			if _, err := s.Statistics(ctx, 7, "UTC", now); err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"activity_minutes", "token_usage"} {
				for _, entry := range []struct {
					minute int64
					want   int
				}{{expired, 0}, {retained, 1}} {
					var count int
					if err := s.db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE minute=?", entry.minute).Scan(&count); err != nil || count != entry.want {
						t.Fatal("expired data survived or shorter view deleted retained data", table, entry, count, err)
					}
				}
			}
		})
	}
}
