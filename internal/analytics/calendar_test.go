package analytics

import (
	"testing"
	"time"
)

func TestCalendarIntervals(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		period, date, zone, start, end string
		hours, buckets                 int
	}{
		{"day", "2026-10-01", "America/Argentina/Buenos_Aires", "2026-10-01T00:00:00-03:00", "2026-10-02T00:00:00-03:00", 24, 24},
		{"week", "2026-10-01", "America/Argentina/Buenos_Aires", "2026-09-28T00:00:00-03:00", "2026-10-05T00:00:00-03:00", 168, 7},
		{"week", "2026-09-27", "UTC", "2026-09-21T00:00:00Z", "2026-09-28T00:00:00Z", 168, 7},
		{"month", "2024-02-29", "UTC", "2024-02-01T00:00:00Z", "2024-03-01T00:00:00Z", 696, 29},
		{"day", "2008-10-19", "America/Argentina/Buenos_Aires", "2008-10-19T01:00:00-02:00", "2008-10-20T00:00:00-02:00", 23, 24},
		{"week", "2008-10-19", "America/Argentina/Buenos_Aires", "2008-10-13T00:00:00-03:00", "2008-10-20T00:00:00-02:00", 167, 7},
		{"day", "2026-03-08", "America/New_York", "2026-03-08T00:00:00-05:00", "2026-03-09T00:00:00-04:00", 23, 24},
		{"day", "2025-11-02", "America/New_York", "2025-11-02T00:00:00-04:00", "2025-11-03T00:00:00-05:00", 25, 24},
	}
	for _, tt := range cases {
		t.Run(tt.period+tt.date+tt.zone, func(t *testing.T) {
			out, err := Resolve(tt.period, tt.date, tt.zone, now)
			if err != nil {
				t.Fatal(err)
			}
			if out.StartAt.Format(time.RFC3339) != tt.start || out.EndAt.Format(time.RFC3339) != tt.end || len(out.Series) != tt.buckets || int(out.EndAt.Sub(out.StartAt).Hours()) != tt.hours {
				t.Fatalf("interval=%+v", out)
			}
			for _, b := range out.Series {
				if b.Accumulations != 0 || b.Redemptions != 0 {
					t.Fatal(b)
				}
			}
		})
	}
}
func TestCalendarRejectsInvalidAndFuture(t *testing.T) {
	now := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC) // still October 1 in brand timezone
	for _, tt := range [][2]string{{"", ""}, {"year", "2026-10-01"}, {"day", "2026-02-30"}, {"day", "2026-1-01"}, {"day", "0000-01-01"}, {"day", "2026-10-02"}} {
		if _, err := Resolve(tt[0], tt[1], "America/Argentina/Buenos_Aires", now); err != ErrInvalidRequest {
			t.Fatalf("%v err=%v", tt, err)
		}
	}
	out, err := Resolve("day", "", "America/Argentina/Buenos_Aires", now)
	if err != nil || out.Date != "2026-10-01" {
		t.Fatalf("default=%+v err=%v", out, err)
	}
	if _, err := Resolve("day", "", "bad/zone", now); err == nil {
		t.Fatal("invalid stored timezone accepted")
	}
}

func TestSkippedCivilDateRejected(t *testing.T) {
	if _, err := Resolve("day", "2011-12-30", "Pacific/Apia", time.Now()); err != ErrInvalidRequest {
		t.Fatalf("skipped date err=%v", err)
	}
	out, err := Resolve("month", "2008-10-19", "America/Argentina/Buenos_Aires", time.Now())
	if err != nil || len(out.Series) != 31 || out.Series[18].Label != "2008-10-19" || out.Series[19].Label != "2008-10-20" {
		t.Fatalf("midnight transition month=%+v err=%v", out, err)
	}
}
