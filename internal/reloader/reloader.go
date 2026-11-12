package reloader

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
)

// Run retries failed reloads without advancing the applied digest.
func Run(ctx context.Context, interval time.Duration, reload func(context.Context) error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			if err := reload(ctx); err != nil && ctx.Err() == nil {
				log.Warn().Err(err).Msg("Schedule reload rejected")
			}
		}
	}
}
