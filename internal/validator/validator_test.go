package validator

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sentoz/vk-scheduler/internal/config"
	"github.com/sentoz/vk-scheduler/internal/executor"
	"github.com/sentoz/vk-scheduler/internal/metrics"
)

type checker struct {
	calls atomic.Int32
	err   error
}

func (c *checker) GetState(context.Context, config.Resource) (string, bool, error) {
	c.calls.Add(1)
	return "stopped", false, c.err
}

func TestGroupedCorrectionSuspensionAndReadFailure(t *testing.T) {
	for _, scenario := range []string{"correct", "suspended", "read-error", "conflict"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
			target := config.Resource{Type: "vm", ID: "test"}
			items := []config.Schedule{{Name: "start", Type: "daily", Resource: target, Actions: config.Actions{Start: &config.ActionConfig{Enabled: true, Time: "09:00"}}}, {Name: "stop", Type: "daily", Resource: target, Actions: config.Actions{Stop: &config.ActionConfig{Enabled: true, Time: "18:00"}}}}
			if scenario == "suspended" {
				items[1].Suspend = true
			}
			if scenario == "conflict" {
				items[1].Actions.Stop.Time = "09:00"
			}
			snapshot, err := config.CompileSnapshot(1, items, time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			seen := make(chan executor.Work, 1)
			e, err := executor.New(1, func() time.Time { return now }, func(_ context.Context, work executor.Work, _ time.Time) { seen <- work })
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				e.CloseAdmission()
				e.Cancel()
				if err := e.Wait(context.Background()); err != nil {
					t.Error(err)
				}
			}()
			c, err := e.BeginCandidate()
			if err != nil {
				t.Fatal(err)
			}
			if err := e.Commit(c, map[string]executor.Work{target.Key(): {Resource: target, Due: func(time.Time, time.Time) bool { return false }, Resolve: func(context.Context, time.Time) (executor.Decision, error) {
				return executor.Decision{Action: "start"}, nil
			}}}, func() {}); err != nil {
				t.Fatal(err)
			}
			m, err := metrics.New()
			if err != nil {
				t.Fatal(err)
			}
			check := &checker{}
			if scenario == "read-error" {
				check.err = errors.New("unavailable")
			}
			Scan(context.Background(), snapshot, check, e, m, now)
			if scenario == "correct" {
				select {
				case <-seen:
				case <-time.After(time.Second):
					t.Fatal("correction lost")
				}
				if check.calls.Load() != 1 {
					t.Fatal("resource not grouped")
				}
			} else {
				e.CloseAdmission()
				if err := e.Wait(context.Background()); err != nil {
					t.Fatal(err)
				}
				select {
				case <-seen:
					t.Fatal("unsafe correction")
				default:
				}
			}
		})
	}
}
