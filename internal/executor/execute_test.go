package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sentoz/vk-scheduler/internal/config"
)

type forbiddenOperator struct{ t *testing.T }

func (o forbiddenOperator) Apply(context.Context, config.Resource, string, func(context.Context) error) (bool, error) {
	o.t.Fatal("dry-run called mutating operator")
	return false, nil
}

type operatorFunc func(context.Context, config.Resource, string, func(context.Context) error) (bool, error)

func (f operatorFunc) Apply(ctx context.Context, r config.Resource, action string, authorize func(context.Context) error) (bool, error) {
	return f(ctx, r, action, authorize)
}

func TestAdmissionRefusalsHaveSpecificOutcomes(t *testing.T) {
	for _, item := range []struct {
		refusal error
		reason  string
	}{{ErrSuperseded, "superseded_before_mutation"}, {ErrResourceRemoved, "resource_removed"}, {ErrClosed, "shutting_down"}, {errors.Join(ErrClosed, errors.New("synthetic transport failure")), ""}} {
		work := Work{Resolve: func(context.Context, time.Time) (Decision, error) { return Decision{Action: "start"}, nil }, Authorize: func(context.Context, string) error { return item.refusal }}
		operator := operatorFunc(func(ctx context.Context, _ config.Resource, _ string, authorize func(context.Context) error) (bool, error) {
			return false, authorize(ctx)
		})
		outcome, err := Execute(context.Background(), work, time.Now(), operator, time.Second, false)
		if item.reason == "" {
			if err == nil || outcome.Status != "error" {
				t.Fatal("joined failure was hidden as skip")
			}
			continue
		}
		if err != nil || outcome.Status != "skipped" || outcome.Reason != item.reason {
			t.Fatal(outcome, err)
		}
	}
}

func TestDryRunNeverCallsOperator(t *testing.T) {
	work := Work{Resource: config.Resource{ID: "test"}, Resolve: func(context.Context, time.Time) (Decision, error) { return Decision{Action: "start"}, nil }, Authorize: func(context.Context, string) error { t.Fatal("dry-run authorized mutation"); return nil }}
	outcome, err := Execute(context.Background(), work, time.Now(), forbiddenOperator{t}, time.Second, true)
	if err != nil || outcome.Status != "dry_run" {
		t.Fatal(outcome, err)
	}
}
