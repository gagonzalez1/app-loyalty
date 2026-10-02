// Package analytics resolves calendar intervals in the merchant's timezone.
package analytics

import (
	"clientesFrecuentes/internal/model"
	"errors"
	"fmt"
	"time"
	_ "time/tzdata"
)

var ErrInvalidRequest = errors.New("invalid analytics period")

func Validate(period, date string) error {
	if period != "day" && period != "week" && period != "month" {
		return ErrInvalidRequest
	}
	if date != "" {
		d, err := time.Parse(time.DateOnly, date)
		if err != nil || d.Format(time.DateOnly) != date || d.Year() < 1 {
			return ErrInvalidRequest
		}
	}
	return nil
}

func Resolve(period, date, timezone string, now time.Time) (model.BrandPeriodMetrics, error) {
	var out model.BrandPeriodMetrics
	if err := Validate(period, date); err != nil {
		return out, err
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return out, fmt.Errorf("brand timezone: %w", err)
	}
	today := now.In(loc).Format(time.DateOnly)
	if date == "" {
		date = today
	}
	if date > today {
		return out, ErrInvalidRequest
	}
	// Keep date arithmetic in UTC civil dates. Local AddDate can move a missing
	// midnight into the preceding day and then duplicate a daily bucket.
	anchor, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return out, ErrInvalidRequest
	}
	startDate := anchor
	switch period {
	case "week":
		startDate = startDate.AddDate(0, 0, -(int(startDate.Weekday())+6)%7)
	case "month":
		startDate = time.Date(startDate.Year(), startDate.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	endDate := startDate.AddDate(0, 0, 1)
	if period == "week" {
		endDate = startDate.AddDate(0, 0, 7)
	}
	if period == "month" {
		endDate = startDate.AddDate(0, 1, 0)
	}
	if localDateStart(anchor, loc).Format(time.DateOnly) != date {
		return out, ErrInvalidRequest
	} // entirely skipped local date
	start, end := localDateStart(startDate, loc), localDateStart(endDate, loc)
	out = model.BrandPeriodMetrics{Period: period, Date: date, Timezone: timezone, StartAt: start, EndAt: end, Series: make([]model.BrandPeriodBucket, 0, 31)}
	if period == "day" {
		for h := 0; h < 24; h++ {
			out.Series = append(out.Series, model.BrandPeriodBucket{Label: fmt.Sprintf("%02d:00", h)})
		}
	} else {
		for d := startDate; d.Before(endDate); d = d.AddDate(0, 0, 1) {
			out.Series = append(out.Series, model.BrandPeriodBucket{Label: d.Format(time.DateOnly)})
		}
	}
	return out, nil
}

// localDateStart finds the first instant at or after a civil date. Some IANA
// zones advance clocks at midnight, so time.Date(00:00) is not a safe boundary.
func localDateStart(date time.Time, loc *time.Location) time.Time {
	label := date.Format(time.DateOnly)
	lo, hi := date.Add(-48*time.Hour).Unix(), date.Add(48*time.Hour).Unix()
	for lo < hi {
		mid := lo + (hi-lo)/2
		if time.Unix(mid, 0).In(loc).Format(time.DateOnly) < label {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return time.Unix(lo, 0).In(loc)
}
