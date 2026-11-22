package schedule

import (
	"testing"
	"time"
)

func stamp(s string) int64 {
	return mustTime(s).UnixMilli()
}
func mustTime(s string) time.Time {
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return v
}

func TestCalendarSkipsNonexistentTimeAndDoesNotRepeatWallSlot(t *testing.T) {
	c, err := (Rule{Kind: "daily", Time: "02:30", Timezone: "America/New_York"}).Parse()
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Next(stamp("2026-03-08T01:30:00-05:00"), 0); got != stamp("2026-03-09T02:30:00-04:00") {
		t.Fatalf("spring gap: %v", time.UnixMilli(got))
	}
	c, err = (Rule{Kind: "daily", Time: "01:00", Timezone: "America/New_York"}).Parse()
	if err != nil {
		t.Fatal(err)
	}
	first := stamp("2026-11-01T01:00:00-04:00")
	second := stamp("2026-11-01T01:00:00-05:00")
	if c.Slot(first) != c.Slot(second) {
		t.Fatal("same wall slot has different keys")
	}
	slot, next, expired := c.Due(first, 0, first)
	if expired || slot != first || next != stamp("2026-11-02T01:00:00-05:00") {
		t.Fatalf("fallback: %d %d %v", slot, next, expired)
	}
}

func TestLatestDueUsesGraceWithoutShiftingCadence(t *testing.T) {
	c, err := (Rule{Kind: "interval", Minutes: 5, Timezone: "UTC"}).Parse()
	if err != nil {
		t.Fatal(err)
	}
	anchor := stamp("2025-01-01T00:00:00Z")
	slot, next, expired := c.Due(c.Next(anchor, anchor), anchor, stamp("2026-11-22T09:17:00Z"))
	if expired || slot != stamp("2026-11-22T09:15:00Z") || next != stamp("2026-11-22T09:20:00Z") {
		t.Fatalf("latest: %d %d %v", slot, next, expired)
	}
	if _, err := (Rule{Kind: "once", At: "2026-03-08T02:30", Timezone: "America/New_York"}).Parse(); err == nil {
		t.Fatal("accepted nonexistent wall time")
	}
}
