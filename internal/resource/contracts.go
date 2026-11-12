package resource

import (
	"context"

	"github.com/sentoz/vk-scheduler/internal/config"
)

// StateChecker reads stable or transitional cloud state.
type StateChecker interface {
	GetState(context.Context, config.Resource) (string, bool, error)
}

// Operator authorizes and executes a single resource mutation.
type Operator interface {
	Apply(context.Context, config.Resource, string, func(context.Context) error) (bool, error)
}
