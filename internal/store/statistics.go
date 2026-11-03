package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
	_ "time/tzdata" // IANA zones also work on Windows without a system zone database.
)

// Retention matches the longest period available in the statistics view.
const StatisticsRetentionDays = 90

var ErrInvalidActivity = errors.New("invalid activity report")

type StatisticsDay struct {
	Date          string `json:"date"`
	ActiveMinutes int64  `json:"activeMinutes"`
	Tokens        int64  `json:"tokens"`
	Requests      int64  `json:"requests"`
	Unreported    int64  `json:"unreported"`
}

type Statistics struct {
	Days          []StatisticsDay `json:"days"`
	Hours         [7][24]float64  `json:"hours"` // Mean active minutes per observed local date/hour, Monday first.
	ActiveMinutes int64           `json:"activeMinutes"`
	ActiveDays    int64           `json:"activeDays"`
	Tokens        int64           `json:"tokens"`
	Requests      int64           `json:"requests"`
	Unreported    int64           `json:"unreported"`
	ActivitySince string          `json:"activitySince"`
	UsageSince    string          `json:"usageSince"`
	SampledAt     string          `json:"sampledAt"`
	Timezone      string          `json:"timezone"`
	RetentionDays int             `json:"retentionDays"`
}

func (s *Store) initStatistics() error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS statistics_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS activity_minutes (minute INTEGER PRIMARY KEY)`,
		`CREATE TABLE IF NOT EXISTS token_usage (id TEXT PRIMARY KEY, minute INTEGER NOT NULL, tokens INTEGER)`,
		`CREATE INDEX IF NOT EXISTS token_usage_by_minute ON token_usage(minute)`,
	} {
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	// One migration only: later usage is collected at provider dispatch, rather
	// than projected again from model_result. Deleted history is not recoverable.
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO statistics_meta(key,value) VALUES('usage_since',?)`, now.Format(time.RFC3339))
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted > 0 {
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO token_usage(id,minute,tokens)
 SELECT 'legacy:'||session_id||':'||sequence, CAST(strftime('%s',created_at) AS INTEGER)/60,
 CASE WHEN json_type(data,'$.modelResult.usage.inputTokens')='integer'
 AND json_type(data,'$.modelResult.usage.outputTokens')='integer'
 AND json_extract(data,'$.modelResult.usage.inputTokens') BETWEEN 0 AND 1000000000
 AND json_extract(data,'$.modelResult.usage.outputTokens') BETWEEN 0 AND 1000000000
 THEN json_extract(data,'$.modelResult.usage.inputTokens')+json_extract(data,'$.modelResult.usage.outputTokens') ELSE NULL END
 FROM events WHERE kind='model_result' AND json_valid(data)
 AND created_at>=? AND strftime('%s',created_at) IS NOT NULL`, now.AddDate(0, 0, -StatisticsRetentionDays).Format(time.RFC3339))
		if err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO statistics_meta(key,value) VALUES('activity_since',?)`, now.Format(time.RFC3339)); err != nil {
		return err
	}
	if err = pruneStatistics(ctx, tx, now); err != nil {
		return err
	}
	return tx.Commit()
}

func pruneStatistics(ctx context.Context, tx *sql.Tx, now time.Time) error {
	cutoff := now.UTC().AddDate(0, 0, -StatisticsRetentionDays).Unix() / 60
	for _, query := range []string{
		`DELETE FROM activity_minutes WHERE minute<?`,
		`DELETE FROM token_usage WHERE minute<?`,
	} {
		if _, err := tx.ExecContext(ctx, query, cutoff); err != nil {
			return err
		}
	}
	return nil
}

// RecordActivity deduplicates UTC minutes across clients. Reports contain only
// minutes with real input, never a heartbeat or the duration of an open page.
func (s *Store) RecordActivity(ctx context.Context, minutes []int64, now time.Time) (bool, error) {
	if len(minutes) == 0 || len(minutes) > 120 {
		return false, ErrInvalidActivity
	}
	latest := now.Unix() / 60
	for _, minute := range minutes {
		if minute < latest-24*60 || minute > latest {
			return false, ErrInvalidActivity
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	changed := false
	for _, minute := range minutes {
		result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO activity_minutes(minute) VALUES(?)`, minute)
		if err != nil {
			return false, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		changed = changed || count > 0
	}
	if err = pruneStatistics(ctx, tx, now); err != nil {
		return false, err
	}
	return changed, tx.Commit()
}

func (s *Store) RecordTokenUsage(ctx context.Context, id string, tokens *int64, now time.Time) (bool, error) {
	if id == "" || len(id) > 128 || tokens != nil && (*tokens < 0 || *tokens > 2_000_000_000) {
		return false, errors.New("invalid token usage")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO token_usage(id,minute,tokens) VALUES(?,?,?)`, id, now.Unix()/60, tokens)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err = pruneStatistics(ctx, tx, now); err != nil {
		return false, err
	}
	return count > 0, tx.Commit()
}

func (s *Store) Statistics(ctx context.Context, days int, zone string, now time.Time) (Statistics, error) {
	var stats Statistics
	if days != 7 && days != 30 && days != StatisticsRetentionDays {
		return stats, errors.New("invalid statistics period")
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return stats, errors.New("invalid statistics timezone")
	}
	local := now.In(location)
	end := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location).AddDate(0, 0, 1)
	start := end.AddDate(0, 0, -days)
	stats.Timezone, stats.RetentionDays = zone, StatisticsRetentionDays
	stats.SampledAt = now.UTC().Format(time.RFC3339)
	indexes := map[string]int{}
	for date := start; date.Before(end); date = date.AddDate(0, 0, 1) {
		key := date.Format("2006-01-02")
		indexes[key] = len(stats.Days)
		stats.Days = append(stats.Days, StatisticsDay{Date: key})
	}
	// One read transaction keeps chart series and totals on the same snapshot.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return stats, err
	}
	defer tx.Rollback()
	for key, target := range map[string]*string{"activity_since": &stats.ActivitySince, "usage_since": &stats.UsageSince} {
		if err = tx.QueryRowContext(ctx, `SELECT value FROM statistics_meta WHERE key=?`, key).Scan(target); err != nil {
			return stats, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT minute FROM activity_minutes WHERE minute>=? AND minute<?`, start.Unix()/60, min(end.Unix()/60, now.Unix()/60+1))
	if err != nil {
		return stats, err
	}
	for rows.Next() {
		var minute int64
		if err = rows.Scan(&minute); err != nil {
			rows.Close()
			return stats, err
		}
		at := time.Unix(minute*60, 0).In(location)
		day := indexes[at.Format("2006-01-02")]
		stats.Days[day].ActiveMinutes++
		stats.Hours[(int(at.Weekday())+6)%7][at.Hour()]++
		stats.ActiveMinutes++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return stats, err
	}
	// Average over observed local date/hour slots, including inactive slots.
	// Repeated DST hours share a slot; skipped and future hours contribute none.
	recordedSince, err := time.Parse(time.RFC3339, stats.ActivitySince)
	if err != nil {
		return stats, err
	}
	observedStart := start
	if recordedSince.After(observedStart) {
		observedStart = recordedSince
	}
	var occurrences [7][24]int
	seen := make(map[[4]int]struct{})
	for at := observedStart.Truncate(time.Minute); !at.After(now); at = at.Add(time.Minute) {
		local := at.In(location)
		year, month, day := local.Date()
		key := [4]int{year, int(month), day, local.Hour()}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		occurrences[(int(local.Weekday())+6)%7][local.Hour()]++
	}
	for day := range stats.Hours {
		for hour := range stats.Hours[day] {
			if count := occurrences[day][hour]; count > 0 {
				stats.Hours[day][hour] /= float64(count)
			} else {
				stats.Hours[day][hour] = 0
			}
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT minute,COALESCE(SUM(tokens),0),COUNT(*),SUM(tokens IS NULL) FROM token_usage WHERE minute>=? AND minute<? GROUP BY minute`, start.Unix()/60, min(end.Unix()/60, now.Unix()/60+1))
	if err != nil {
		return stats, err
	}
	for rows.Next() {
		var minute, tokens, requests, unreported int64
		if err = rows.Scan(&minute, &tokens, &requests, &unreported); err != nil {
			rows.Close()
			return stats, err
		}
		day := &stats.Days[indexes[time.Unix(minute*60, 0).In(location).Format("2006-01-02")]]
		day.Tokens += tokens
		day.Requests += requests
		day.Unreported += unreported
		stats.Tokens += tokens
		stats.Requests += requests
		stats.Unreported += unreported
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return stats, err
	}
	for _, day := range stats.Days {
		if day.ActiveMinutes > 0 {
			stats.ActiveDays++
		}
	}
	return stats, tx.Commit()
}
