package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sentoz/vk-scheduler/internal/config"
	"github.com/sentoz/vk-scheduler/internal/executor"
	"github.com/sentoz/vk-scheduler/internal/metrics"
	"github.com/sentoz/vk-scheduler/internal/reloader"
	"github.com/sentoz/vk-scheduler/internal/resource"
	"github.com/sentoz/vk-scheduler/internal/schedule"
	"github.com/sentoz/vk-scheduler/internal/scheduler"
	"github.com/sentoz/vk-scheduler/internal/validator"
	"github.com/sentoz/vk-scheduler/internal/vkcloud"
	"github.com/sentoz/vk-scheduler/internal/web"
)

// Cloud is the narrow lifecycle surface used by the application.
type Cloud interface {
	resource.StateChecker
	resource.Operator
	PrepareServices([]config.Resource) error
	PendingOperations() []vkcloud.PendingOperation
	ObservePending(context.Context, config.Resource) (bool, error)
	Shutdown(context.Context) error
}

// Options controls the process and supplies small deterministic test seams.
type Options struct {
	DryRun         bool
	Address        string
	ReloadInterval time.Duration
	Now            func() time.Time
}

type timerScheduler interface {
	Prepare(*config.Snapshot, func(uint64, string, time.Time) bool) ([]uuid.UUID, error)
	Remove([]uuid.UUID) ([]uuid.UUID, error)
	Start()
	Shutdown() error
}

// App owns snapshots, all background loops, and the single shutdown sequence.
type App struct {
	cfg       config.Config
	cloud     Cloud
	options   Options
	location  *time.Location
	current   atomic.Pointer[config.Snapshot]
	executor  *executor.Executor
	scheduler timerScheduler
	web       *web.Server
	metrics   *metrics.Metrics
	control   context.Context
	cancel    context.CancelFunc
	loops     sync.WaitGroup
	reloadMu  sync.Mutex
	digest    [32]byte
	applied   bool
	timers    []uuid.UUID
	orphans   []uuid.UUID
	run       atomic.Bool
}

// New binds HTTP and prepares components without starting cloud operations.
func New(cfg config.Config, cloud Cloud, options Options) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cloud == nil {
		return nil, config.ErrInvalidConfig
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ReloadInterval == 0 {
		options.ReloadInterval = 10 * time.Second
	}
	if options.ReloadInterval < 0 {
		return nil, config.ErrInvalidConfig
	}
	if options.Address == "" {
		options.Address = net.JoinHostPort("", strconv.Itoa(cfg.MetricsPort))
	}
	location, err := cfg.Location()
	if err != nil {
		return nil, err
	}
	m, err := metrics.New()
	if err != nil {
		return nil, err
	}
	control, cancel := context.WithCancel(context.Background())
	a := &App{cfg: cfg, cloud: cloud, options: options, location: location, control: control, cancel: cancel, metrics: m}
	a.scheduler, err = scheduler.New(location, cleanupReserve(cfg.ShutdownTimeout.Std()))
	if err != nil {
		cancel()
		return nil, err
	}
	a.executor, err = executor.New(cfg.MaxConcurrentJobs, options.Now, a.execute)
	if err != nil {
		cancel()
		return nil, errors.Join(err, a.scheduler.Shutdown())
	}
	a.web, err = web.New(control, options.Address, cfg, a.Snapshot, cloud, m)
	if err != nil {
		cancel()
		a.executor.CloseAdmission()
		a.executor.Cancel()
		return nil, errors.Join(err, a.executor.Wait(context.Background()), a.scheduler.Shutdown())
	}
	return a, nil
}

// Snapshot returns the immutable current generation.
func (a *App) Snapshot() *config.Snapshot { return a.current.Load() }

// Address returns the HTTP listener address.
func (a *App) Address() string { return a.web.Address() }

func cleanupReserve(timeout time.Duration) time.Duration { return min(5*time.Second, timeout/5) }

// Run starts the application and joins all cooperative components on cancellation.
func (a *App) Run(ctx context.Context) error {
	if !a.run.CompareAndSwap(false, true) {
		return ErrAlreadyRun
	}
	a.web.Start()
	// Startup and control loops cancel with the process; admitted work has its own root.
	stopSignal := context.AfterFunc(ctx, a.cancel)
	defer stopSignal()
	if err := a.Reload(a.control); err != nil {
		return errors.Join(fmt.Errorf("startup schedules: %w", err), a.shutdown())
	}
	a.scheduler.Start()
	a.startLoop(a.observe)
	if a.cfg.ValidationResources {
		validator.Scan(a.control, a.Snapshot(), a.cloud, a.executor, a.metrics, a.options.Now())
	}
	if a.control.Err() != nil {
		return a.shutdown()
	}
	a.startLoop(func() { reloader.Run(a.control, a.options.ReloadInterval, a.Reload) })
	if a.cfg.ValidationResources {
		a.startLoop(func() {
			ticker := time.NewTicker(a.cfg.ValidationInterval.Std())
			defer ticker.Stop()
			for {
				select {
				case <-a.control.Done():
					return
				case <-ticker.C:
					validator.Scan(a.control, a.Snapshot(), a.cloud, a.executor, a.metrics, a.options.Now())
				}
			}
		})
	}
	a.web.SetReady(true)
	log.Info().Uint64("generation", a.Snapshot().Generation).Msg("VK scheduler ready")
	var failure error
	select {
	case <-ctx.Done():
	case failure = <-a.web.Errors():
	}
	return errors.Join(failure, a.shutdown())
}

func (a *App) startLoop(run func()) { a.loops.Add(1); go func() { defer a.loops.Done(); run() }() }

// Reload atomically publishes a fully captured and preflighted candidate.
func (a *App) Reload(ctx context.Context) error {
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(a.orphans) > 0 {
		failed, err := a.scheduler.Remove(a.orphans)
		a.orphans = failed
		if err != nil {
			log.Warn().Err(err).Msg("Retry timer cleanup")
		}
	}
	bundle, err := config.CaptureBundle(a.cfg.SchedulesDir)
	if err != nil {
		return err
	}
	if a.applied && bundle.Digest == a.digest {
		return nil
	}
	candidate, err := a.executor.BeginCandidate()
	if err != nil {
		return err
	}
	defer a.executor.Abort(candidate)
	items, err := config.ParseBundle(bundle, a.cfg.Cloud)
	if err != nil {
		return err
	}
	location := a.location
	snapshot, err := config.CompileSnapshot(candidate.Generation(), items, location)
	if err != nil {
		return err
	}
	if err := snapshot.ValidateCalendars(a.options.Now()); err != nil {
		return err
	}
	resources := make([]config.Resource, 0, len(snapshot.Groups))
	for _, group := range snapshot.Groups {
		resources = append(resources, group.Resource)
	}
	if err := a.cloud.PrepareServices(resources); err != nil {
		return err
	}
	if err := a.preflight(ctx, resources); err != nil {
		return err
	}
	plans := map[string]executor.Work{}
	for key, group := range snapshot.Groups {
		if group.Suspended {
			continue
		}
		plans[key] = executor.Work{Resource: group.Resource, Due: func(after, through time.Time) bool {
			for _, action := range group.Actions {
				if action.Calendar.NeedsResolve(after, through, location) {
					return true
				}
			}
			return false
		}, Resolve: func(ctx context.Context, now time.Time) (executor.Decision, error) {
			desired, err := schedule.ResolveDesired(ctx, group.Actions, now, location)
			return executor.Decision{Action: desired.Action, At: desired.At, ScheduleName: desired.ScheduleName}, err
		}}
	}
	ids, err := a.scheduler.Prepare(snapshot, a.executor.Notify)
	cleanup := func() {
		failed, removeErr := a.scheduler.Remove(ids)
		a.orphans = append(a.orphans, failed...)
		if removeErr != nil {
			log.Warn().Err(removeErr).Msg("Candidate timer cleanup failed")
		}
	}
	if err != nil {
		cleanup()
		return err
	}
	if err := ctx.Err(); err != nil {
		cleanup()
		return err
	}
	if err := a.executor.Commit(candidate, plans, func() { a.current.Store(snapshot) }); err != nil {
		cleanup()
		return err
	}
	old := a.timers
	a.timers = ids
	a.digest = bundle.Digest
	a.applied = true
	failed, removeErr := a.scheduler.Remove(old)
	a.orphans = append(a.orphans, failed...)
	if removeErr != nil {
		log.Warn().Err(removeErr).Msg("Old timer cleanup failed")
	}
	log.Info().Uint64("generation", snapshot.Generation).Int("schedules", len(items)).Msg("Schedules published")
	return nil
}

func (a *App) preflight(ctx context.Context, resources []config.Resource) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan config.Resource)
	failures := make(chan error, 1)
	var workers sync.WaitGroup
	for range min(a.cfg.Cloud.MaxConcurrentReads, len(resources)) {
		workers.Go(func() {
			for target := range jobs {
				_, _, err := a.cloud.GetState(ctx, target)
				if err != nil && !errors.Is(err, vkcloud.ErrResourceFailed) && !errors.Is(err, vkcloud.ErrUnsupportedState) && !errors.Is(err, vkcloud.ErrUnexpectedState) {
					select {
					case failures <- fmt.Errorf("preflight %s %s in project %s region %s: %w", target.Type, target.ID, target.ProjectID, target.Region, err):
					default:
					}
					cancel()
					return
				}
			}
		})
	}
send:
	for _, target := range resources {
		select {
		case jobs <- target:
		case <-ctx.Done():
			break send
		}
	}
	close(jobs)
	workers.Wait()
	select {
	case err := <-failures:
		return err
	default:
		return ctx.Err()
	}
}

func (a *App) execute(ctx context.Context, work executor.Work, at time.Time) {
	started := time.Now()
	outcome, err := executor.Execute(ctx, work, at, a.cloud, a.cfg.OperationTimeout.Std(), a.options.DryRun)
	if errors.Is(err, schedule.ErrScheduleConflict) {
		outcome.Status = "skipped"
		outcome.Reason = "schedule_conflict"
	}
	a.metrics.Outcome(work.Resource.Type, outcome)
	event := log.Info()
	if err != nil {
		event = log.Error().Err(err)
	}
	event.Str("schedule", outcome.Decision.ScheduleName).Uint64("generation", work.Generation).Dur("duration", time.Since(started)).Str("resource_type", work.Resource.Type).Str("resource_id", work.Resource.ID).Str("project_id", work.Resource.ProjectID).Str("region", work.Resource.Region).Str("action", outcome.Decision.Action).Str("status", outcome.Status).Str("reason", outcome.Reason).Msg("Resource intent completed")
}

func (a *App) observe() {
	ticker := time.NewTicker(a.cfg.Cloud.PollInterval.Std())
	defer ticker.Stop()
	lastWarning := time.Time{}
	lastRecoveryWarning := time.Time{}
	for {
		for _, record := range a.cloud.PendingOperations() {
			if a.control.Err() != nil {
				return
			}
			ok, err := a.cloud.ObservePending(a.control, record.Resource)
			if a.control.Err() != nil {
				return
			}
			// An active lease or an unconfirmed stable state is expected; API failures are not.
			if err != nil && err != vkcloud.ErrOperationConflict && err != vkcloud.ErrUnconfirmedOperation {
				now := a.options.Now()
				if now.Sub(lastRecoveryWarning) >= time.Minute {
					log.Warn().Err(err).Str("resource_id", record.Resource.ID).Str("resource_type", record.Resource.Type).Str("project_id", record.Resource.ProjectID).Str("region", record.Resource.Region).Msg("Pending operation observation failed")
					lastRecoveryWarning = now
				}
			}
			if ok && err == nil {
				a.executor.NotifyRecovery(record.Resource.Key())
			}
		}
		now := a.options.Now()
		age := a.executor.OldestPendingAge(now)
		a.metrics.Oldest.Set(age.Seconds())
		a.metrics.Pending.Set(float64(a.executor.PendingCount()))
		a.metrics.Unconfirmed.Set(float64(len(a.cloud.PendingOperations())))
		if age > 60*time.Second && now.Sub(lastWarning) >= time.Minute {
			log.Warn().Dur("pending_age", age).Msg("Resource intent delivery delayed")
			lastWarning = now
		}
		select {
		case <-a.control.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) shutdown() error {
	timeout := a.cfg.ShutdownTimeout.Std()
	hard, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	a.web.SetReady(false)
	a.executor.CloseAdmission()
	a.cancel()
	deadline, _ := hard.Deadline()
	results := make(chan error, 4)
	go func() { results <- executor.Drain(hard, a.executor, deadline.Add(-cleanupReserve(timeout))) }()
	go func() { results <- a.scheduler.Shutdown() }()
	go func() { results <- a.web.Shutdown(hard) }()
	go func() { a.loops.Wait(); results <- nil }()
	var result error
components:
	for range 4 {
		select {
		case err := <-results:
			result = errors.Join(result, err)
		case <-hard.Done():
			result = errors.Join(result, hard.Err())
			break components
		}
	}
	cloudDone := make(chan error, 1)
	go func() { cloudDone <- a.cloud.Shutdown(hard) }()
	select {
	case err := <-cloudDone:
		return errors.Join(result, err)
	case <-hard.Done():
		return errors.Join(result, hard.Err())
	}
}
