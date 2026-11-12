package validator

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sentoz/vk-scheduler/internal/config"
	"github.com/sentoz/vk-scheduler/internal/executor"
	"github.com/sentoz/vk-scheduler/internal/metrics"
	"github.com/sentoz/vk-scheduler/internal/resource"
	"github.com/sentoz/vk-scheduler/internal/schedule"
)

// Scan compares each unsuspended resource with its latest unambiguous event.
func Scan(ctx context.Context, snapshot *config.Snapshot, checker resource.StateChecker, e *executor.Executor, m *metrics.Metrics, now time.Time) {
	if snapshot == nil {
		return
	}
	for key, g := range snapshot.Groups {
		if ctx.Err() != nil {
			return
		}
		if g.Suspended {
			continue
		}
		desired, err := schedule.ResolveDesired(ctx, g.Actions, now, snapshot.Location)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Warn().Err(err).Str("resource_id", g.Resource.ID).Msg("Cannot resolve desired state")
			continue
		}
		actual, transitional, err := checker.GetState(ctx, g.Resource)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Warn().Err(err).Str("resource_id", g.Resource.ID).Msg("Resource validation failed")
			continue
		}
		state := "running"
		if desired.Action == "stop" {
			state = "stopped"
		}
		if !transitional && actual != state && e.NotifyCorrection(snapshot.Generation, key) {
			m.Correction(g.Resource.Type, desired.Action)
		}
	}
}
