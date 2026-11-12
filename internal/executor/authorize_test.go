package executor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sentoz/vk-scheduler/internal/config"
)

type preparingOperator struct {
	before func(string)
	posted chan string
}

func (o preparingOperator) Apply(ctx context.Context, _ config.Resource, action string,
	authorize func(context.Context) error) (bool, error) {
	o.before(action)
	if err := authorize(ctx); err != nil {
		return false, err
	}
	o.posted <- action
	return true, nil
}

func TestSupersededBeforeMutation(t *testing.T) {
	var clock atomic.Int64
	var generation atomic.Uint64
	outcomes := make(chan Outcome, 2)
	posted := make(chan string, 2)
	var e *Executor
	work := workFor("start", 1)
	work.Due = func(after, through time.Time) bool {
		for _, second := range []int64{1, 2} {
			at := time.Unix(second, 0)
			if at.After(after) && !at.After(through) {
				return true
			}
		}
		return false
	}
	work.Resolve = func(_ context.Context, at time.Time) (Decision, error) {
		action := "start"
		if at.Unix() >= 2 {
			action = "stop"
		}
		return Decision{Action: action, At: at}, nil
	}
	operator := preparingOperator{posted: posted, before: func(action string) {
		if action == "start" {
			clock.Store(2)
			e.Notify(generation.Load(), work.Resource.Key(), time.Unix(2, 0))
		}
	}}
	e = newTestExecutor(t, &clock, func(ctx context.Context, current Work, at time.Time) {
		outcome, err := Execute(ctx, current, at, operator, time.Second, false)
		if err != nil {
			t.Error(err)
		}
		outcomes <- outcome
	})
	c := commitWork(t, e, work)
	generation.Store(c.Generation())
	clock.Store(1)
	e.Notify(c.Generation(), work.Resource.Key(), time.Unix(1, 0))
	first, second := receive(t, outcomes), receive(t, outcomes)
	if first.Status != "skipped" || second.Status != "success" {
		t.Fatalf("outcomes=%+v %+v", first, second)
	}
	if got := receive(t, posted); got != "stop" {
		t.Fatalf("stale mutation: %s", got)
	}
	if len(posted) != 0 {
		t.Fatal("more than one mutation authorized")
	}
}

func TestEmptyCommitStopsNewManagement(t *testing.T) {
	var clock atomic.Int64
	e := newTestExecutor(t, &clock, func(context.Context, Work, time.Time) { t.Error("unexpected work") })
	work := workFor("start", 10)
	old := commitWork(t, e, work)
	empty, err := e.BeginCandidate()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Commit(empty, map[string]Work{}, func() {}); err != nil {
		t.Fatal(err)
	}
	if e.Notify(old.Generation(), work.Resource.Key(), time.Unix(20, 0)) {
		t.Fatal("old timer accepted")
	}
	if e.NotifyRecovery(work.Resource.Key()) {
		t.Fatal("removed target recovered into mutation")
	}
}
