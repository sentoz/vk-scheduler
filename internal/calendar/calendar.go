package calendar

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/sentoz/vk-scheduler/internal/config"
	"github.com/sentoz/vk-scheduler/internal/schedule"
)

// Event preserves the frontend's collection format and cloud resource identity.
type Event struct {
	ScheduleName     string    `json:"schedule_name"`
	DisplayName      string    `json:"schedule_display_name"`
	ResourceType     string    `json:"resource_type"`
	ResourceID       string    `json:"resource_id"`
	ProjectID        string    `json:"project_id"`
	Region           string    `json:"region"`
	ResourceKey      string    `json:"resource_key"`
	Action           string    `json:"action"`
	Time             time.Time `json:"time"`
	LocalDate        string    `json:"local_date"`
	LocalTime        string    `json:"local_time"`
	State            string    `json:"state"`
	Transitional     bool      `json:"transitional"`
	StatusError      string    `json:"status_error,omitempty"`
	Suspended        bool      `json:"suspended"`
	OperationPending bool      `json:"operation_pending"`
}

// Range returns an inclusive date range as a half-open instant interval.
func Range(from, to string, now time.Time, location *time.Location) (time.Time, time.Time, error) {
	if from == "" && to == "" {
		local := now.In(location)
		from = local.Format(time.DateOnly)
		to = local.AddDate(0, 0, 6).Format(time.DateOnly)
	}
	start, err := time.Parse(time.DateOnly, from)
	if err != nil {
		return time.Time{}, time.Time{}, ErrInvalidRange
	}
	last, err := time.Parse(time.DateOnly, to)
	if err != nil || last.Before(start) || !last.Before(start.AddDate(0, 0, 366)) {
		return time.Time{}, time.Time{}, ErrInvalidRange
	}
	return schedule.DayStart(start, location), schedule.DayStart(last.AddDate(0, 0, 1), location), nil
}

// Events expands the same compiled calendars used by timers and reconciliation.
func Events(ctx context.Context, snapshot *config.Snapshot, start, end time.Time) ([]Event, error) {
	result := make([]Event, 0)
	// Reserve the response envelope and bounded status fields added after expansion.
	responseBytes := 1024
	names := make(map[string]config.Schedule, len(snapshot.Schedules))
	for _, item := range snapshot.Schedules {
		names[item.Name] = item
	}
	for key, group := range snapshot.Groups {
		for _, action := range group.Actions {
			item := names[action.ScheduleName]
			cursor := start.Add(-time.Nanosecond)
			for {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				next := action.Calendar.Next(cursor, snapshot.Location)
				if next.IsZero() {
					break
				}
				if !next.After(cursor) {
					return nil, ErrInvalidRange
				}
				if !next.Before(end) {
					break
				}
				if len(result) >= 100000 {
					return nil, ErrEventLimit
				}
				event := Event{ScheduleName: item.Name, DisplayName: item.DisplayName, ResourceType: group.Resource.Type, ResourceID: group.Resource.ID, ProjectID: group.Resource.ProjectID, Region: group.Resource.Region, ResourceKey: key, Action: action.Action, Time: next, LocalDate: next.Format(time.DateOnly), LocalTime: next.Format(time.TimeOnly), Suspended: group.Suspended}
				encoded, err := json.Marshal(event)
				if err != nil {
					return nil, ErrInvalidRange
				}
				responseBytes += len(encoded) + 128
				if responseBytes > 64<<20 {
					return nil, ErrResponseLimit
				}
				result = append(result, event)
				cursor = next
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Time.Equal(result[j].Time) {
			if result[i].ScheduleName == result[j].ScheduleName {
				return result[i].Action < result[j].Action
			}
			return result[i].ScheduleName < result[j].ScheduleName
		}
		return result[i].Time.Before(result[j].Time)
	})
	return result, nil
}
