package workstats

import (
	"testing"
	"time"
)

// A day in any time zone in use is a whole run of quarters, India's and
// Nepal's too; a day that changes clocks has 25 hours (or 23); the whole
// range spans its buckets.
func TestBuckets(t *testing.T) {
	for _, tz := range []string{"Asia/Kolkata", "Asia/Kathmandu", "Pacific/Chatham", "UTC"} {
		loc, _ := time.LoadLocation(tz)
		days, whole, err := Buckets(tz, "2026-03-04", 2, false)
		if err != nil {
			t.Fatal(err)
		}
		mid := time.Date(2026, 3, 4, 0, 0, 0, 0, loc)
		if len(days) != 2 || days[0].From*QuarterSeconds != mid.Unix() || days[0].To != days[1].From || whole != (Bound{days[0].From, days[1].To}) {
			t.Fatalf("%s: %v, whole %v", tz, days, whole)
		}
	}
	hours, _, err := Buckets("America/New_York", "2026-11-01", 1, true)
	if err != nil || len(hours) != 25 {
		t.Fatalf("the day clocks go back: %d hours, %v", len(hours), err)
	}
	days, _, _ := Buckets("America/New_York", "2026-10-31", 3, false)
	if n := days[1].To - days[1].From; n != 100 {
		t.Fatalf("Nov 1 in New York: %d quarters", n)
	}
	for _, c := range []struct {
		tz, day string
		days    int
		hourly  bool
	}{
		{"Mars/Olympus", "2026-03-04", 1, false},
		{"", "2026-03-04", 1, false},
		{"UTC", "today", 1, false},
		{"UTC", "2026-03-04", 0, false},
		{"UTC", "2026-03-04", MaxDays + 1, false},
		{"UTC", "2026-03-04", MaxHourDays + 1, true},
	} {
		if _, _, err := Buckets(c.tz, c.day, c.days, c.hourly); err == nil {
			t.Fatalf("no error for %+v", c)
		}
	}
}

// A turn's seconds fall into the quarters it was in progress in; one that
// ends at a quarter's first instant is not in it; one that took no time is
// in the quarter it ended in.
func TestSpread(t *testing.T) {
	at := func(h, m, s int) time.Time { return time.Date(2026, 3, 4, h, m, s, 0, time.UTC) }
	q := func(h, m int) int64 { return Quarter(at(h, m, 0)) }
	if got := Spread(at(10, 14, 50), at(10, 15, 5)); len(got) != 2 || got[q(10, 0)] != 10 || got[q(10, 15)] != 5 {
		t.Fatalf("over a quarter's edge: %v", got)
	}
	if got := Spread(at(10, 0, 0), at(10, 15, 0)); len(got) != 1 || got[q(10, 0)] != 900 {
		t.Fatalf("up to the next quarter: %v", got)
	}
	if got := Spread(at(10, 15, 0), at(10, 15, 0)); len(got) != 1 || got[q(10, 15)] != 0 {
		t.Fatalf("no time: %v", got)
	}
	if got := Spread(at(10, 20, 0), at(10, 10, 0)); len(got) != 1 || got[q(10, 0)] != 0 {
		t.Fatalf("start after end (a clock that went back): %v", got)
	}
}
