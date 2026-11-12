package schedule

import (
	"context"
	"time"
)

// ResourceAction associates a compiled calendar action with its manifest.
type ResourceAction struct {
	ScheduleName string
	Action       string
	Calendar     CompiledAction
}

// Desired describes the most recent unambiguous event for a resource.
type Desired struct {
	At           time.Time
	Action       string
	ScheduleName string
}

// ResolveDesired merges all enabled actions of one resource.
func ResolveDesired(ctx context.Context, actions []ResourceAction,
	now time.Time, location *time.Location) (Desired, error) {
	var latest Desired
	conflict := false
	for _, action := range actions {
		if action.Action != "start" && action.Action != "stop" {
			return Desired{}, ErrInvalidScheduleType
		}
		previous, err := action.Calendar.Previous(ctx, now, location)
		if err != nil {
			return Desired{}, err
		}
		if latest.At.IsZero() || previous.After(latest.At) {
			latest = Desired{At: previous, Action: action.Action, ScheduleName: action.ScheduleName}
			conflict = false
		} else if previous.Equal(latest.At) && action.Action != latest.Action {
			conflict = true
		}
	}
	if conflict {
		return Desired{}, ErrScheduleConflict
	}
	if latest.At.IsZero() {
		return Desired{}, ErrNoPreviousOccurrence
	}
	return latest, nil
}
