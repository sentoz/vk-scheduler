package executor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sentoz/vk-scheduler/internal/schedule"
)

func TestReloadDoesNotMoveCalendarWatermarkBackwards(t *testing.T) {
	var clock atomic.Value
	now := time.Now()
	clock.Store(now)
	e, err := New(1, func() time.Time { return clock.Load().(time.Time) }, func(context.Context, Work, time.Time) { t.Error("unexpected work") })
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
	work := workFor("start", 1)
	work.Due = func(time.Time, time.Time) bool { return false }
	first := commitWork(t, e, work)
	watermark := now.Add(time.Minute)
	e.Notify(first.Generation(), work.Resource.Key(), watermark)
	e.mu.Lock()
	stored := e.observed[work.Resource.Key()]
	e.mu.Unlock()
	if stored != stored.Round(0) {
		t.Fatal("calendar retained monotonic-clock ordering")
	}
	clock.Store(now.Add(-time.Hour))
	current := commitWork(t, e, work)
	e.mu.Lock()
	stored = e.observed[work.Resource.Key()]
	e.mu.Unlock()
	if !stored.Equal(watermark) {
		t.Fatal("reload moved watermark backwards", stored, watermark)
	}
	e.Notify(current.Generation(), work.Resource.Key(), now)
	e.mu.Lock()
	stored = e.observed[work.Resource.Key()]
	e.mu.Unlock()
	if !stored.Equal(watermark) {
		t.Fatal("backward callback moved watermark")
	}
}

func TestForwardClockJumpBeyondCronLookaheadRechecksDesired(t *testing.T) {
	before := time.Date(2096, 3, 1, 12, 0, 0, 0, time.UTC)
	after := time.Date(2104, 2, 29, 12, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(before.Unix())
	var resolves atomic.Int32
	var calendars []schedule.ResourceAction
	for _, entry := range []struct{ action, expression string }{{"start", "0 9 29 2 *"}, {"stop", "0 18 29 2 *"}} {
		compiled, err := schedule.CompileAction("cron", "", entry.expression, nil)
		if err != nil {
			t.Fatal(err)
		}
		calendars = append(calendars, schedule.ResourceAction{Action: entry.action, Calendar: compiled})
	}
	if !calendars[0].Calendar.Next(before, time.UTC).IsZero() {
		t.Fatal("fixture no longer crosses pinned parser lookahead")
	}
	work := workFor("stop", 1)
	work.Due = func(from, through time.Time) bool {
		for _, action := range calendars {
			if action.Calendar.NeedsResolve(from, through, time.UTC) {
				return true
			}
		}
		return false
	}
	work.Resolve = func(ctx context.Context, at time.Time) (Decision, error) {
		desired, err := schedule.ResolveDesired(ctx, calendars, at, time.UTC)
		if resolves.Add(1) == 2 {
			clock.Store(after.Unix())
		}
		return Decision{Action: desired.Action, At: desired.At}, err
	}
	outcomes := make(chan Outcome, 2)
	posts := make(chan string, 2)
	operator := preparingOperator{before: func(string) {}, posted: posts}
	e := newTestExecutor(t, &clock, func(ctx context.Context, work Work, at time.Time) {
		out, err := Execute(ctx, work, at, operator, time.Second, false)
		if err != nil {
			t.Error(err)
		}
		outcomes <- out
	})
	candidate := commitWork(t, e, work)
	e.NotifyCorrection(candidate.Generation(), work.Resource.Key())
	first, second := receive(t, outcomes), receive(t, outcomes)
	if first.Status != "skipped" || second.Status != "success" {
		t.Fatal(first, second)
	}
	if got := receive(t, posts); got != "start" || len(posts) != 0 {
		t.Fatal("stale stop crossed the parser horizon", got)
	}
}

func TestBackwardClockJumpDuringResolveCannotAuthorizeStaleAction(t *testing.T) {
	var clock atomic.Int64
	clock.Store(3)
	var resolves atomic.Int32
	outcomes := make(chan Outcome, 2)
	posts := make(chan string, 2)
	work := workFor("stop", 1)
	work.Due = func(time.Time, time.Time) bool { return false }
	work.Resolve = func(_ context.Context, at time.Time) (Decision, error) {
		action := "start"
		if at.Unix() >= 2 {
			action = "stop"
		}
		if resolves.Add(1) == 2 {
			clock.Store(1)
		}
		return Decision{Action: action, At: at}, nil
	}
	operator := preparingOperator{posted: posts, before: func(string) {}}
	e := newTestExecutor(t, &clock, func(ctx context.Context, work Work, at time.Time) {
		out, err := Execute(ctx, work, at, operator, time.Second, false)
		if err != nil {
			t.Error(err)
		}
		outcomes <- out
	})
	candidate := commitWork(t, e, work)
	if !e.NotifyCorrection(candidate.Generation(), work.Resource.Key()) {
		t.Fatal("intent not accepted")
	}
	first, second := receive(t, outcomes), receive(t, outcomes)
	if first.Status != "skipped" || second.Status != "success" {
		t.Fatal(first, second)
	}
	if got := receive(t, posts); got != "start" || len(posts) != 0 {
		t.Fatal("stale stop authorized across clock reversal", got)
	}
}
