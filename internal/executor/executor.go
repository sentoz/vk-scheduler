package executor

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/sentoz/vk-scheduler/internal/config"
	"github.com/sentoz/vk-scheduler/internal/resource"
)

// Decision identifies the calendar action selected for a reserved intent.
type Decision struct {
	Action       string
	At           time.Time
	ScheduleName string
}

// Work binds a resource plan and its worker-local authorization callback.
type Work struct {
	Generation uint64
	Resource   config.Resource
	Due        func(after, through time.Time) bool
	Resolve    func(context.Context, time.Time) (Decision, error)
	Authorize  func(context.Context, string) error
}

// Candidate identifies one non-reusable reload attempt.
type Candidate struct {
	owner      *Executor
	generation uint64
	base       uint64
	used       bool
}

// Generation returns the unique ID captured by candidate timer callbacks.
func (c *Candidate) Generation() uint64 { return c.generation }

// Executor coalesces notifications and owns resource admission and concurrency.
type Executor struct {
	mu           sync.Mutex
	next         uint64
	current      uint64
	accepting    bool
	plans        map[string]Work
	observed     map[string]time.Time
	pending      map[string]bool
	pendingSince map[string]time.Time
	active       map[string]*lease
	queued       map[string]bool
	ready        []string
	wake         chan struct{}
	stop         chan struct{}
	done         chan struct{}
	workers      sync.WaitGroup
	root         context.Context
	cancel       context.CancelFunc
	run          func(context.Context, Work, time.Time)
	now          func() time.Time
}

// New starts the sole worker pool; run must honor cancellation.
func New(limit int, now func() time.Time, run func(context.Context, Work, time.Time)) (*Executor, error) {
	if limit < 1 || now == nil || run == nil {
		return nil, ErrInvalidExecutor
	}
	root, cancel := context.WithCancel(context.Background())
	e := &Executor{
		accepting: true, plans: make(map[string]Work), observed: make(map[string]time.Time),
		pending: make(map[string]bool), pendingSince: make(map[string]time.Time), active: make(map[string]*lease), queued: make(map[string]bool),
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
		root: root, cancel: cancel, run: run, now: now,
	}
	e.workers.Add(limit)
	for range limit {
		go e.worker()
	}
	go func() { e.workers.Wait(); close(e.done) }()
	return e, nil
}

// BeginCandidate reserves an ID even when the attempt is later aborted.
func (e *Executor) BeginCandidate() (*Candidate, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.accepting {
		return nil, ErrClosed
	}
	if e.next == ^uint64(0) {
		return nil, ErrGenerationExhausted
	}
	e.next++
	return &Candidate{owner: e, generation: e.next, base: e.current}, nil
}

// Abort permanently prevents publication of a failed candidate.
func (e *Executor) Abort(c *Candidate) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c != nil && c.owner == e {
		c.used = true
	}
}

// Commit publishes prepared plans and a read-model pointer under the admission mutex.
func (e *Executor) Commit(c *Candidate, plans map[string]Work, publish func()) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c == nil || c.owner != e || c.used {
		return ErrInvalidCandidate
	}
	c.used = true
	if !e.accepting {
		return ErrClosed
	}
	if c.base != e.current {
		return ErrStaleCandidate
	}
	if publish == nil {
		return ErrInvalidCandidate
	}
	for key, work := range plans {
		if key != work.Resource.Key() || work.Due == nil || work.Resolve == nil {
			return ErrInvalidCandidate
		}
	}
	cutover := e.now().Round(0)
	pending := make(map[string]bool)
	observed := make(map[string]time.Time, len(plans))
	ownedPlans := make(map[string]Work, len(plans))
	for key, work := range plans {
		old, exists := e.plans[key]
		if exists && (e.pending[key] || e.active[key] != nil || old.Due(e.observed[key], cutover)) {
			pending[key] = true
		}
		observed[key] = cutover
		if exists && e.observed[key].After(cutover) {
			observed[key] = e.observed[key]
		}
		work.Generation = c.generation
		ownedPlans[key] = work
	}
	e.current = c.generation
	ages := make(map[string]time.Time, len(pending))
	for key := range pending {
		since, exists := e.pendingSince[key]
		if !exists {
			since = cutover
		}
		ages[key] = since
	}
	e.plans, e.observed, e.pending = ownedPlans, observed, pending
	e.pendingSince = ages
	e.ready = nil
	e.queued = make(map[string]bool)
	for key := range pending {
		e.enqueueLocked(key)
	}
	publish()
	return nil
}

// Notify records calendar events without waiting for cloud execution.
func (e *Executor) Notify(generation uint64, key string, through time.Time) bool {
	// Calendar watermarks use wall time; monotonic ordering hides backward clock jumps.
	through = through.Round(0)
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.accepting || generation != e.current {
		return false
	}
	work, exists := e.plans[key]
	if !exists || !through.After(e.observed[key]) {
		return false
	}
	due := work.Due(e.observed[key], through)
	e.observed[key] = through
	if due {
		e.markPendingLocked(key)
	}
	return due
}

// NotifyCorrection queues drift repair through the same coalescing mailbox.
func (e *Executor) NotifyCorrection(generation uint64, key string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.accepting || generation != e.current {
		return false
	}
	if _, exists := e.plans[key]; !exists {
		return false
	}
	fresh := !e.pending[key]
	e.markPendingLocked(key)
	return fresh
}

func (e *Executor) enqueueLocked(key string) {
	if e.active[key] != nil || e.queued[key] {
		return
	}
	e.queued[key] = true
	e.ready = append(e.ready, key)
	e.signalLocked()
}

func (e *Executor) signalLocked() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *Executor) worker() {
	defer e.workers.Done()
	for {
		select {
		case <-e.stop:
			return
		case <-e.wake:
		}
		e.mu.Lock()
		if !e.accepting {
			e.mu.Unlock()
			return
		}
		if len(e.ready) == 0 {
			e.mu.Unlock()
			continue
		}
		key := e.ready[0]
		e.ready = e.ready[1:]
		delete(e.queued, key)
		delete(e.pending, key)
		delete(e.pendingSince, key)
		work := e.plans[key]
		ticket := &lease{key: key, generation: e.current}
		e.active[key] = ticket
		work.Authorize = func(ctx context.Context, action string) error { return e.authorize(ctx, ticket, action) }
		admittedAt := e.now()
		if len(e.ready) > 0 {
			e.signalLocked()
		}
		e.mu.Unlock()

		e.run(e.root, work, admittedAt)

		e.mu.Lock()
		delete(e.active, key)
		if e.accepting && e.pending[key] {
			e.enqueueLocked(key)
		}
		e.mu.Unlock()
	}
}

// lease separates resource reservation from the authorization of its first POST.
type lease struct {
	key        string
	generation uint64
	mutating   bool
}

func (e *Executor) authorize(ctx context.Context, ticket *lease, action string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		e.mu.Lock()
		if !e.accepting {
			e.mu.Unlock()
			return ErrClosed
		}
		if e.active[ticket.key] != ticket || ticket.mutating {
			e.mu.Unlock()
			return ErrInvalidPermit
		}
		work, exists := e.plans[ticket.key]
		if !exists {
			e.mu.Unlock()
			return ErrResourceRemoved
		}
		generation := e.current
		e.mu.Unlock()

		checkedAt := e.now().Round(0)
		decision, err := work.Resolve(ctx, checkedAt)
		if err != nil {
			return err
		}

		e.mu.Lock()
		if !e.accepting {
			e.mu.Unlock()
			return ErrClosed
		}
		if e.active[ticket.key] != ticket || ticket.mutating {
			e.mu.Unlock()
			return ErrInvalidPermit
		}
		current, exists := e.plans[ticket.key]
		if !exists {
			e.mu.Unlock()
			return ErrResourceRemoved
		}
		currentTime := e.now().Round(0)
		if generation != e.current || currentTime.Before(checkedAt) || current.Due(checkedAt, currentTime) {
			e.mu.Unlock()
			continue
		}
		if generation != ticket.generation || decision.Action != action {
			e.markPendingLocked(ticket.key)
			e.mu.Unlock()
			return ErrSuperseded
		}
		if err := ctx.Err(); err != nil {
			e.mu.Unlock()
			return err
		}
		ticket.mutating = true
		e.mu.Unlock()
		return nil
	}
}

// NotifyRecovery resumes an owned intent after its old cloud operation is confirmed.
func (e *Executor) NotifyRecovery(key string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.accepting {
		return false
	}
	if _, exists := e.plans[key]; !exists {
		return false
	}
	e.markPendingLocked(key)
	return true
}

func (e *Executor) markPendingLocked(key string) {
	if !e.pending[key] {
		e.pendingSince[key] = e.now()
	}
	e.pending[key] = true
	e.enqueueLocked(key)
}

// OldestPendingAge reports delivery delay without treating queue time as operation time.
func (e *Executor) OldestPendingAge(now time.Time) time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	var oldest time.Duration
	for _, since := range e.pendingSince {
		if age := now.Sub(since); age > oldest {
			oldest = age
		}
	}
	return oldest
}

// PendingCount reports the number of resources waiting in the mailbox.
func (e *Executor) PendingCount() int { e.mu.Lock(); defer e.mu.Unlock(); return len(e.pending) }

// CloseAdmission drops unadmitted work and lets active workers drain.
func (e *Executor) CloseAdmission() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.accepting {
		return
	}
	e.accepting = false
	e.pending = make(map[string]bool)
	e.pendingSince = make(map[string]time.Time)
	e.ready = nil
	e.queued = make(map[string]bool)
	close(e.stop)
}

// Wait joins the worker pool within the caller's deadline.
func (e *Executor) Wait(ctx context.Context) error {
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Cancel interrupts active work during the cleanup reserve.
func (e *Executor) Cancel() { e.cancel() }

// Outcome records the terminal disposition of one admitted resource intent.
type Outcome struct {
	Decision Decision
	Status   string
	Reason   string
}

// Execute resolves a reserved intent; the client authorizes its first POST after GET.
func Execute(ctx context.Context, work Work, reservedAt time.Time,
	operator resource.Operator, timeout time.Duration, dryRun bool) (Outcome, error) {
	outcome := Outcome{Status: "error"}
	if timeout <= 0 || work.Authorize == nil {
		return outcome, ErrInvalidExecutor
	}
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	decision, err := work.Resolve(operationCtx, reservedAt)
	outcome.Decision = decision
	if err != nil {
		return outcome, err
	}
	if decision.Action != "start" && decision.Action != "stop" {
		return outcome, ErrUnsupportedAction
	}
	if dryRun {
		outcome.Status = "dry_run"
		return outcome, nil
	}
	changed, err := operator.Apply(operationCtx, work.Resource, decision.Action,
		func(ctx context.Context) error { return work.Authorize(ctx, decision.Action) })
	// Only a standalone authorization refusal is a skip; joined failures remain errors.
	if err == ErrSuperseded || err == ErrResourceRemoved || err == ErrClosed {
		outcome.Status, outcome.Reason = "skipped", "superseded_before_mutation"
		switch err {
		case ErrResourceRemoved:
			outcome.Reason = "resource_removed"
		case ErrClosed:
			outcome.Reason = "shutting_down"
		}
		return outcome, nil
	}
	if err != nil {
		return outcome, err
	}
	outcome.Status = "success"
	if !changed {
		outcome.Status, outcome.Reason = "skipped", "already_in_state"
	}
	return outcome, nil
}

// Drain waits until the drain boundary, then cancels work and joins by the hard deadline.
func Drain(hard context.Context, e *Executor, drainAt time.Time) error {
	e.CloseAdmission()
	drain, cancel := context.WithDeadline(hard, drainAt)
	defer cancel()
	first := e.Wait(drain)
	e.Cancel()
	if first == nil {
		return nil
	}
	return errors.Join(first, e.Wait(hard))
}
