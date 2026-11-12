package scheduler

import (
	"errors"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
	"github.com/sentoz/vk-scheduler/internal/config"
)

// Scheduler owns only lightweight calendar notifications.
type Scheduler struct{ clock gocron.Scheduler }

// New creates a timer scheduler without any cloud concurrency queue.
func New(location *time.Location, stopTimeout time.Duration) (*Scheduler, error) {
	s, err := gocron.NewScheduler(gocron.WithLocation(location), gocron.WithStopTimeout(stopTimeout))
	if err != nil {
		return nil, err
	}
	return &Scheduler{clock: s}, nil
}

// Prepare registers candidate callbacks whose generation is initially inactive.
func (s *Scheduler) Prepare(snapshot *config.Snapshot, notify func(uint64, string, time.Time) bool) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	for key, group := range snapshot.Groups {
		if group.Suspended {
			continue
		}
		for _, action := range group.Actions {
			job, err := s.clock.NewJob(gocron.CronJob(action.Calendar.Expression, action.Calendar.Seconds), gocron.NewTask(func() { notify(snapshot.Generation, key, time.Now()) }))
			if err != nil {
				return ids, err
			}
			ids = append(ids, job.ID())
		}
	}
	return ids, nil
}

// Remove removes only the supplied generation's timer IDs.
func (s *Scheduler) Remove(ids []uuid.UUID) ([]uuid.UUID, error) {
	var failed []uuid.UUID
	var result error
	for _, id := range ids {
		if err := s.clock.RemoveJob(id); err != nil {
			failed = append(failed, id)
			result = errors.Join(result, err)
		}
	}
	return failed, result
}

// Start begins notification delivery.
func (s *Scheduler) Start() { s.clock.Start() }

// Shutdown joins notifier callbacks exactly once under App ownership.
func (s *Scheduler) Shutdown() error { return s.clock.Shutdown() }
