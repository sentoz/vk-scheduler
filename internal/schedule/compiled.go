package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// CompiledAction shares a normalized expression across all calendar consumers.
type CompiledAction struct {
	Expression string
	Seconds    bool
	Schedule   cron.Schedule
}

// ParseClock accepts exactly HH:MM or HH:MM:SS.
func ParseClock(value string) (int, int, int, error) {
	if len(value) != 5 && len(value) != 8 {
		return 0, 0, 0, ErrInvalidClock
	}
	if value[2] != ':' || (len(value) == 8 && value[5] != ':') {
		return 0, 0, 0, ErrInvalidClock
	}
	parts := strings.Split(value, ":")
	values := [3]int{}
	for index, part := range parts {
		if len(part) != 2 || part[0] < '0' || part[0] > '9' || part[1] < '0' || part[1] > '9' {
			return 0, 0, 0, ErrInvalidClock
		}
		number, err := strconv.Atoi(part)
		if err != nil {
			return 0, 0, 0, ErrInvalidClock
		}
		values[index] = number
	}
	if values[0] > 23 || values[1] > 59 || values[2] > 59 {
		return 0, 0, 0, ErrInvalidClock
	}
	return values[0], values[1], values[2], nil
}

// CompileAction normalizes an enabled action after manifest validation.
func CompileAction(kind, clock, expression string, day *int) (CompiledAction, error) {
	seconds := kind != "cron"
	if seconds {
		hour, minute, second, err := ParseClock(clock)
		if err != nil {
			return CompiledAction{}, err
		}
		switch kind {
		case "daily":
			expression = fmt.Sprintf("%d %d %d * * *", second, minute, hour)
		case "weekly":
			if day == nil || *day < 0 || *day > 6 {
				return CompiledAction{}, ErrInvalidDay
			}
			expression = fmt.Sprintf("%d %d %d * * %d", second, minute, hour, *day)
		case "monthly":
			if day == nil || *day < 1 || *day > 31 {
				return CompiledAction{}, ErrInvalidDay
			}
			expression = fmt.Sprintf("%d %d %d %d * *", second, minute, hour, *day)
		default:
			return CompiledAction{}, ErrInvalidScheduleType
		}
	} else if len(strings.Fields(expression)) != 5 {
		return CompiledAction{}, ErrInvalidCron
	}
	options := cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow
	if seconds {
		options |= cron.Second
	}
	parsed, err := cron.NewParser(options).Parse(expression)
	if err != nil {
		return CompiledAction{}, fmt.Errorf("compile action: %w", ErrInvalidCron)
	}
	return CompiledAction{Expression: expression, Seconds: seconds, Schedule: parsed}, nil
}

// Next returns the next instant strictly after the supplied boundary.
func (c CompiledAction) Next(after time.Time, location *time.Location) time.Time {
	return c.Schedule.Next(after.In(location))
}

// NeedsResolve reports whether forward wall time may have crossed an event boundary.
func (c CompiledAction) NeedsResolve(after, through time.Time, location *time.Location) bool {
	after, through = after.In(location), through.In(location)
	if !through.After(after) {
		return false
	}
	next := c.Next(after, location)
	if next.IsZero() {
		// Pinned robfig/cron stops searching after five years; zero proves nothing beyond that.
		return through.Year() > after.Year()+5
	}
	return !next.After(after) || !next.After(through)
}
