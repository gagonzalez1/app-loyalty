package service

import (
	"testing"
	"time"
)

func TestBranchProrationCalendar(t *testing.T) {
	loc := argentinaLocation()
	tests := []struct {
		name, now, next         string
		anchor, days, remaining int
		unit, amount            int64
		trial                   bool
	}{
		{"October15", "2026-10-15", "2026-11-01", 1, 31, 17, 2500000, 1370968, false},
		{"due day", "2026-11-01", "2026-11-01", 1, 30, 30, 2500000, 2500000, false},
		{"February anchor31", "2027-02-15", "2027-02-28", 31, 28, 13, 2500000, 1160714, false},
		{"LeapFebruary anchor31", "2028-02-15", "2028-02-29", 31, 29, 14, 2500000, 1206897, false},
		{"discount", "2026-10-15", "2026-11-01", 1, 31, 17, 1250000, 685484, false},
		{"minorunits", "2026-10-31", "2026-11-01", 1, 31, 1, 159, 5, false},
		{"halfcent", "2026-11-16", "2026-12-01", 1, 30, 15, 159, 80, false},
		{"free trial", "2026-10-15", "2026-11-01", 1, 31, 17, 2500000, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now, _ := time.ParseInLocation("2006-01-02", tt.now, loc)
			next, _ := time.ParseInLocation("2006-01-02", tt.next, loc)
			var trial *time.Time
			if tt.trial {
				x := next
				trial = &x
			}
			remaining, days, amount, _, _, e := anchoredCalendarProration(now, next, tt.unit, trial, tt.anchor)
			if e != nil || remaining != tt.remaining || days != tt.days || amount != tt.amount {
				t.Fatalf("got %d/%d %d err=%v want %d/%d %d", remaining, days, amount, e, tt.remaining, tt.days, tt.amount)
			}
		})
	}
}
func TestBranchCalendarRejectUnknownCycle(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	next := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	if _, _, _, _, _, e := anchoredCalendarProration(now, next, 100, nil, 1); e == nil {
		t.Fatal("unproven cycle accepted")
	}
}
