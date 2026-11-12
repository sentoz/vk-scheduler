package config

import (
	"fmt"
	"time"

	"github.com/sentoz/vk-scheduler/internal/schedule"
)

// Group merges all calendars and suspension overrides for one resource.
type Group struct {
	Resource  Resource
	Actions   []schedule.ResourceAction
	Suspended bool
}

// Snapshot is immutable after publication to runtime consumers.
type Snapshot struct {
	Generation uint64
	Schedules  []Schedule
	Groups     map[string]Group
	Location   *time.Location
}

// CompileSnapshot builds privately owned groups from validated schedules.
func CompileSnapshot(generation uint64, schedules []Schedule, location *time.Location) (*Snapshot, error) {
	if location == nil {
		return nil, ErrInvalidConfig
	}
	s := &Snapshot{Generation: generation, Location: location, Groups: map[string]Group{}}
	for _, item := range schedules {
		// Own action pointers so a caller cannot mutate published calendars.
		clone := func(a *ActionConfig) *ActionConfig {
			if a == nil {
				return nil
			}
			b := *a
			if a.Day != nil {
				d := *a.Day
				b.Day = &d
			}
			return &b
		}
		item.Actions = Actions{Start: clone(item.Actions.Start), Stop: clone(item.Actions.Stop)}
		s.Schedules = append(s.Schedules, item)
		key := item.Resource.Key()
		g := s.Groups[key]
		g.Resource = item.Resource
		g.Suspended = g.Suspended || item.Suspend
		for _, a := range []struct {
			name string
			cfg  *ActionConfig
		}{{"start", item.Actions.Start}, {"stop", item.Actions.Stop}} {
			if a.cfg == nil || !a.cfg.Enabled {
				continue
			}
			compiled, err := schedule.CompileAction(item.Type, a.cfg.Time, a.cfg.Crontab, a.cfg.Day)
			if err != nil {
				return nil, fmt.Errorf("compile schedule: %w", err)
			}
			g.Actions = append(g.Actions, schedule.ResourceAction{ScheduleName: item.Name, Action: a.name, Calendar: compiled})
		}
		s.Groups[key] = g
	}
	return s, nil
}

// ValidateCalendars rejects timers that the scheduler cannot register before network preflight.
func (s *Snapshot) ValidateCalendars(now time.Time) error {
	sources := make(map[string]Schedule, len(s.Schedules))
	for _, item := range s.Schedules {
		sources[item.Name] = item
	}
	for _, group := range s.Groups {
		if group.Suspended {
			continue
		}
		for _, action := range group.Actions {
			next := action.Calendar.Next(now, s.Location)
			if next.IsZero() || !next.After(now) {
				source := sources[action.ScheduleName]
				return fmt.Errorf("manifest %q document %d field spec.actions.%s: %w", source.sourceFile, source.sourceDocument, action.Action, schedule.ErrNoNextOccurrence)
			}
		}
	}
	return nil
}
