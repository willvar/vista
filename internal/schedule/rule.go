// Package schedule calculates future occurrences. Persistence and execution belong to the Server.
package schedule

import (
	"errors"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

const (
	Grace    = 10 * time.Minute
	RunLimit = 30 * time.Minute
)

var ErrRule = errors.New("invalid timing rule")

type Rule struct {
	Kind     string `json:"kind"`
	Timezone string `json:"timezone"`
	At       string `json:"at,omitempty"`
	Minutes  int    `json:"minutes,omitempty"`
	Time     string `json:"time,omitempty"`
	Weekday  int    `json:"weekday,omitempty"` // Sunday = 0.
}

type Calendar struct {
	kind string
	at   int64
	step int64
	zone *time.Location
	plan cron.Schedule
}

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func (r Rule) Parse() (Calendar, error) {
	c := Calendar{kind: r.Kind}
	if r.Timezone == "" || r.Timezone == "Local" || len(r.Timezone) > 120 {
		return c, ErrRule
	}
	zone, err := time.LoadLocation(r.Timezone)
	if err != nil {
		return c, ErrRule
	}
	c.zone = zone
	switch r.Kind {
	case "once":
		for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05"} {
			at, err := time.ParseInLocation(layout, r.At, zone)
			if err == nil && at.Format(layout) == r.At && at.Year() >= 1970 {
				c.at = at.UnixMilli()
				return c, nil
			}
		}
	case "interval":
		if r.Minutes >= 1 && r.Minutes <= 365*24*60 {
			c.step = int64(r.Minutes) * time.Minute.Milliseconds()
			return c, nil
		}
	case "daily", "weekly":
		wall, err := time.Parse("15:04", r.Time)
		if err != nil || wall.Format("15:04") != r.Time || r.Weekday < 0 || r.Weekday > 6 {
			break
		}
		day := "*"
		if r.Kind == "weekly" {
			day = fmt.Sprint(r.Weekday)
		}
		c.plan, err = parser.Parse(fmt.Sprintf("%d %d * * %s", wall.Minute(), wall.Hour(), day))
		if err == nil {
			return c, nil
		}
	}
	return c, ErrRule
}

// Next returns the first occurrence strictly after after, or zero for an
// exhausted one-shot rule. Intervals remain aligned to the creation anchor.
func (c Calendar) Next(after, anchor int64) int64 {
	switch c.kind {
	case "once":
		if c.at > after {
			return c.at
		}
	case "interval":
		if after < anchor {
			return anchor
		}
		return anchor + ((after-anchor)/c.step+1)*c.step
	default:
		next := c.plan.Next(time.UnixMilli(after).In(c.zone))
		if !next.IsZero() {
			return next.UnixMilli()
		}
	}
	return 0
}

// Slot uses wall time for calendar schedules. Both occurrences of a repeated
// local clock time have the same key, unlike a fixed elapsed-time interval.
func (c Calendar) Slot(at int64) string {
	if c.plan != nil {
		return "calendar/" + time.UnixMilli(at).In(c.zone).Format("2006-01-02T15:04")
	}
	return fmt.Sprintf("time/%d", at)
}

// Due coalesces missed occurrences without walking the entire offline period.
// expired is true when no occurrence was due within the grace window.
func (c Calendar) Due(cursor, anchor, now int64) (slot, next int64, expired bool) {
	start := now - Grace.Milliseconds()
	slot = cursor
	if slot < start {
		slot = c.Next(start-1, anchor)
	}
	next = c.Next(now, anchor)
	if slot == 0 || slot > now {
		if next != 0 && c.Slot(cursor) == c.Slot(next) {
			next = c.Next(next, anchor)
		}
		return cursor, next, true
	}
	for candidate := c.Next(slot, anchor); candidate > 0 && candidate <= now; candidate = c.Next(slot, anchor) {
		slot = candidate
	}
	if next != 0 && c.Slot(next) == c.Slot(slot) {
		next = c.Next(next, anchor)
	}
	return slot, next, false
}
