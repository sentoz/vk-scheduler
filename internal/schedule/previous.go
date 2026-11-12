package schedule

import (
	"context"
	"time"
)

// Previous returns the latest occurrence at or before now within eight years.
func (c CompiledAction) Previous(ctx context.Context, now time.Time, location *time.Location) (time.Time, error) {
	local := now.In(location)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	earliest := day.AddDate(-8, 0, 0)
	for !day.Before(earliest) {
		if err := ctx.Err(); err != nil {
			return time.Time{}, err
		}
		start := DayStart(day, location)
		nextDay := DayStart(day.AddDate(0, 0, 1), location)
		upper := nextDay.Add(-time.Nanosecond)
		if now.Before(upper) {
			upper = now
		}
		lower := start.Add(-time.Second)
		first := c.Next(lower, location)
		if !first.IsZero() && !first.After(lower) {
			return time.Time{}, ErrInvalidCron
		}
		if !first.IsZero() && !first.After(upper) {
			best := first
			left, right := lower.Unix(), upper.Unix()
			for left <= right {
				if err := ctx.Err(); err != nil {
					return time.Time{}, err
				}
				middle := left + (right-left)/2
				boundary := time.Unix(middle, 0)
				candidate := c.Next(boundary, location)
				if !candidate.IsZero() && !candidate.After(boundary) {
					return time.Time{}, ErrInvalidCron
				}
				if !candidate.IsZero() && !candidate.After(upper) {
					best = candidate
					left = middle + 1
				} else {
					right = middle - 1
				}
			}
			return best, nil
		}
		day = day.AddDate(0, 0, -1)
	}
	return time.Time{}, ErrNoPreviousOccurrence
}

// DayStart returns the first instant on or after a civil date, including midnight jumps.
func DayStart(date time.Time, location *time.Location) time.Time {
	y, m, d := date.Date()
	civil := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	// Searching absolute time avoids AddDate looping on a normalized nonexistent midnight.
	left, right := civil.Add(-36*time.Hour).Unix(), civil.Add(36*time.Hour).Unix()
	for left < right {
		middle := left + (right-left)/2
		local := time.Unix(middle, 0).In(location)
		ly, lm, ld := local.Date()
		value := time.Date(ly, lm, ld, 0, 0, 0, 0, time.UTC)
		if value.Before(civil) {
			left = middle + 1
		} else {
			right = middle
		}
	}
	return time.Unix(left, 0).In(location)
}
