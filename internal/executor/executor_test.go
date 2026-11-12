package executor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sentoz/vk-scheduler/internal/config"
)

func newTestExecutor(t *testing.T, clock *atomic.Int64,
	run func(context.Context, Work, time.Time)) *Executor {
	t.Helper()
	e, err := New(1, func() time.Time { return time.Unix(clock.Load(), 0) }, run)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		e.CloseAdmission()
		e.Cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.Wait(ctx); err != nil {
			t.Error(err)
		}
	})
	return e
}

func workFor(action string, eventAt int64) Work {
	return Work{
		Resource: config.Resource{Type: "vm", ID: "test", ProjectID: "project", Region: "region"},
		Due: func(after, through time.Time) bool {
			event := time.Unix(eventAt, 0)
			return event.After(after) && !event.After(through)
		},
		Resolve: func(context.Context, time.Time) (Decision, error) {
			return Decision{Action: action, At: time.Unix(eventAt, 0)}, nil
		},
	}
}

func commitWork(t *testing.T, e *Executor, work Work) *Candidate {
	t.Helper()
	c, err := e.BeginCandidate()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Commit(c, map[string]Work{work.Resource.Key(): work}, func() {}); err != nil {
		t.Fatal(err)
	}
	return c
}

func receive[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not progress")
	}
	var zero T
	return zero
}

func TestAbortedGenerationNeverBecomesCurrent(t *testing.T) {
	var clock atomic.Int64
	e := newTestExecutor(t, &clock, func(context.Context, Work, time.Time) {})
	failed, err := e.BeginCandidate()
	if err != nil {
		t.Fatal(err)
	}
	e.Abort(failed)
	work := workFor("start", 1)
	current := commitWork(t, e, work)
	if current.Generation() <= failed.Generation() {
		t.Fatal("candidate ID reused")
	}
	clock.Store(2)
	if e.Notify(failed.Generation(), work.Resource.Key(), time.Unix(2, 0)) {
		t.Fatal("orphan timer became active")
	}
	if err := e.Commit(failed, map[string]Work{work.Resource.Key(): work}, func() {}); !errors.Is(err, ErrInvalidCandidate) {
		t.Fatal("aborted candidate published")
	}
}

func TestStopDuringStartIsDeliveredWithoutValidator(t *testing.T) {
	var clock atomic.Int64
	actions := make(chan string, 2)
	release := make(chan struct{})
	e := newTestExecutor(t, &clock, func(ctx context.Context, work Work, at time.Time) {
		decision, err := work.Resolve(ctx, at)
		if err != nil {
			t.Error(err)
			return
		}
		if err := work.Authorize(ctx, decision.Action); err != nil {
			t.Error(err)
			return
		}
		actions <- decision.Action
		if decision.Action == "start" {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	})
	work := workFor("start", 1)
	work.Due = func(after, through time.Time) bool {
		for _, second := range []int64{1, 2} {
			event := time.Unix(second, 0)
			if event.After(after) && !event.After(through) {
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
	current := commitWork(t, e, work)
	clock.Store(1)
	e.Notify(current.Generation(), work.Resource.Key(), time.Unix(1, 0))
	if got := receive(t, actions); got != "start" {
		t.Fatalf("got %s", got)
	}
	clock.Store(2)
	e.Notify(current.Generation(), work.Resource.Key(), time.Unix(2, 0))
	close(release)
	if got := receive(t, actions); got != "stop" {
		t.Fatalf("lost stop: got %s", got)
	}
}

func TestCapacityKeepsOnePendingCorrection(t *testing.T) {
	var clock atomic.Int64
	entered := make(chan string, 3)
	release := make(chan struct{})
	e := newTestExecutor(t, &clock, func(ctx context.Context, work Work, _ time.Time) {
		entered <- work.Resource.ID
		if work.Resource.ID == "a" {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	})
	a, b := workFor("start", 1), workFor("stop", 1)
	a.Resource.ID, b.Resource.ID = "a", "b"
	c, err := e.BeginCandidate()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Commit(c, map[string]Work{a.Resource.Key(): a, b.Resource.Key(): b}, func() {}); err != nil {
		t.Fatal(err)
	}
	e.NotifyCorrection(c.Generation(), a.Resource.Key())
	receive(t, entered)
	for range 100 {
		e.NotifyCorrection(c.Generation(), b.Resource.Key())
	}
	e.mu.Lock()
	count, queueLength := len(e.pending), len(e.ready)
	e.mu.Unlock()
	if count != 1 || queueLength != 1 {
		t.Fatalf("pending=%d ready=%d", count, queueLength)
	}
	close(release)
	if got := receive(t, entered); got != "b" {
		t.Fatalf("lost correction: got %s", got)
	}
}

func TestWorkerPoolKeepsGlobalLimit(t *testing.T) {
	var clock atomic.Int64
	var running atomic.Int32
	var maximum atomic.Int32
	entered := make(chan string, 3)
	release := make(chan struct{})
	e, err := New(2, func() time.Time { return time.Unix(clock.Load(), 0) },
		func(ctx context.Context, work Work, _ time.Time) {
			active := running.Add(1)
			defer running.Add(-1)
			for {
				previous := maximum.Load()
				if previous >= active || maximum.CompareAndSwap(previous, active) {
					break
				}
			}
			entered <- work.Resource.ID
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		e.CloseAdmission()
		e.Cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.Wait(ctx); err != nil {
			t.Error(err)
		}
	})
	plans := make(map[string]Work)
	for _, id := range []string{"a", "b", "c"} {
		work := workFor("start", 1)
		work.Resource.ID = id
		plans[work.Resource.Key()] = work
	}
	c, err := e.BeginCandidate()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Commit(c, plans, func() {}); err != nil {
		t.Fatal(err)
	}
	for key := range plans {
		e.NotifyCorrection(c.Generation(), key)
	}
	receive(t, entered)
	receive(t, entered)
	e.mu.Lock()
	activeCount, pendingCount := len(e.active), len(e.pending)
	e.mu.Unlock()
	if activeCount != 2 || pendingCount != 1 {
		t.Fatalf("active=%d pending=%d", activeCount, pendingCount)
	}
	close(release)
	receive(t, entered)
	if maximum.Load() > 2 {
		t.Fatal("worker limit exceeded")
	}
}

func TestReloadCapturesEventBeforeOldTimerCallback(t *testing.T) {
	var clock atomic.Int64
	actions := make(chan string, 1)
	e := newTestExecutor(t, &clock, func(ctx context.Context, work Work, at time.Time) {
		decision, err := work.Resolve(ctx, at)
		if err != nil {
			t.Error(err)
			return
		}
		if err := work.Authorize(ctx, decision.Action); err != nil {
			t.Error(err)
			return
		}
		actions <- decision.Action
	})
	old := workFor("start", 2)
	oldCandidate := commitWork(t, e, old)
	clock.Store(3)
	fresh := workFor("stop", 1)
	commitWork(t, e, fresh)
	if e.Notify(oldCandidate.Generation(), old.Resource.Key(), time.Unix(3, 0)) {
		t.Fatal("stale callback accepted")
	}
	if got := receive(t, actions); got != "stop" {
		t.Fatalf("new desired state not used: %s", got)
	}
}

func TestAuthorizedOldMutationCanFinish(t *testing.T) {
	var clock atomic.Int64
	actions := make(chan string, 2)
	release := make(chan struct{})
	e := newTestExecutor(t, &clock, func(ctx context.Context, work Work, at time.Time) {
		decision, err := work.Resolve(ctx, at)
		if err != nil {
			t.Error(err)
			return
		}
		if err := work.Authorize(ctx, decision.Action); err != nil {
			t.Error(err)
			return
		}
		actions <- decision.Action
		if decision.Action == "start" {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	})
	old := workFor("start", 1)
	c := commitWork(t, e, old)
	clock.Store(1)
	e.Notify(c.Generation(), old.Resource.Key(), time.Unix(1, 0))
	receive(t, actions)
	clock.Store(2)
	commitWork(t, e, workFor("stop", 1))
	close(release)
	if got := receive(t, actions); got != "stop" {
		t.Fatalf("rebase failed: %s", got)
	}
}

func TestDrainCancelsBeforeHardDeadlineAndJoins(t *testing.T) {
	var clock atomic.Int64
	started := make(chan struct{})
	cancelled := make(chan struct{})
	e := newTestExecutor(t, &clock, func(ctx context.Context, _ Work, _ time.Time) {
		close(started)
		<-ctx.Done()
		close(cancelled)
	})
	work := workFor("start", 1)
	c := commitWork(t, e, work)
	e.NotifyCorrection(c.Generation(), work.Resource.Key())
	receive(t, started)
	hard, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := Drain(hard, e, time.Now().Add(-time.Second))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	receive(t, cancelled)
	if hard.Err() != nil {
		t.Fatal("cleanup consumed the hard deadline")
	}
	if err := e.Wait(hard); err != nil {
		t.Fatal(err)
	}
}

func TestPendingAgeSurvivesCoalescingAndCommit(t *testing.T) {
	var clock atomic.Int64
	entered := make(chan struct{})
	e := newTestExecutor(t, &clock, func(ctx context.Context, _ Work, _ time.Time) {
		close(entered)
		<-ctx.Done()
	})
	a, b := workFor("start", 1), workFor("stop", 1)
	a.Resource.ID, b.Resource.ID = "a", "b"
	plans := map[string]Work{a.Resource.Key(): a, b.Resource.Key(): b}
	c, err := e.BeginCandidate()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Commit(c, plans, func() {}); err != nil {
		t.Fatal(err)
	}
	e.NotifyCorrection(c.Generation(), a.Resource.Key())
	receive(t, entered)
	clock.Store(1)
	e.NotifyCorrection(c.Generation(), b.Resource.Key())
	clock.Store(11)
	e.NotifyCorrection(c.Generation(), b.Resource.Key())
	next, err := e.BeginCandidate()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Commit(next, plans, func() {}); err != nil {
		t.Fatal(err)
	}
	if age := e.OldestPendingAge(time.Unix(11, 0)); age != 10*time.Second {
		t.Fatalf("pending age reset: %s", age)
	}
}

func TestMetadataOnlyReloadDoesNotCreateAnIdleIntent(t *testing.T) {
	var clock atomic.Int64
	e := newTestExecutor(t, &clock, func(context.Context, Work, time.Time) { t.Error("metadata-only reload created work") })
	work := workFor("start", 100)
	commitWork(t, e, work)
	clock.Store(10)
	commitWork(t, e, work)
	if e.PendingCount() != 0 {
		t.Fatal("idle resource gained a pending intent")
	}
}
