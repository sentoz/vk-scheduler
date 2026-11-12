package schedule

import "errors"

var (
	// ErrNoNextOccurrence rejects calendars outside the pinned parser's forward horizon.
	ErrNoNextOccurrence = errors.New("no future occurrence within the supported cron horizon")
	// ErrInvalidClock reports that time must be HH:MM or HH:MM:SS.
	ErrInvalidClock = errors.New("time must be HH:MM or HH:MM:SS")
	// ErrInvalidDay reports that day is outside schedule range.
	ErrInvalidDay = errors.New("day is outside schedule range")
	// ErrInvalidScheduleType reports that unsupported schedule type.
	ErrInvalidScheduleType = errors.New("unsupported schedule type")
	// ErrInvalidCron reports that cron must have five valid fields.
	ErrInvalidCron = errors.New("cron must have five valid fields")
	// ErrNoPreviousOccurrence reports that no previous occurrence within eight years.
	ErrNoPreviousOccurrence = errors.New("no previous occurrence within eight years")
	// ErrScheduleConflict reports that simultaneous opposite actions.
	ErrScheduleConflict = errors.New("simultaneous opposite actions")
)
