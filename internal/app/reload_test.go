package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sentoz/vk-scheduler/internal/config"
)

var (
	errTestRegistration = errors.New("synthetic registration failure")
	errTestCleanup      = errors.New("synthetic timer cleanup failure")
)

type failingTimers struct {
	timerScheduler
	failPrepare   bool
	failCleanup   bool
	notifications map[uint64]func(time.Time) bool
}

func (f *failingTimers) Prepare(snapshot *config.Snapshot, notify func(uint64, string, time.Time) bool) ([]uuid.UUID, error) {
	for key := range snapshot.Groups {
		f.notifications[snapshot.Generation] = func(at time.Time) bool { return notify(snapshot.Generation, key, at) }
	}
	ids, err := f.timerScheduler.Prepare(snapshot, notify)
	if err == nil && f.failPrepare {
		err = errTestRegistration
	}
	return ids, err
}

func (f *failingTimers) Remove(ids []uuid.UUID) ([]uuid.UUID, error) {
	if f.failCleanup && len(ids) > 0 {
		return ids, errTestCleanup
	}
	return f.timerScheduler.Remove(ids)
}

func TestFailedRegistrationAndCleanupCannotActivateAbortedGeneration(t *testing.T) {
	cfg := testConfig(t)
	cfg.ValidationResources = false
	path := filepath.Join(cfg.SchedulesDir, "schedule.yaml")
	publish := func(name string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(strings.Replace(testManifest, "name: test", "name: "+name, 1)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	publish("initial")
	a, err := New(cfg, &blockingCloud{}, Options{Address: "127.0.0.1:0", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	fake := &failingTimers{timerScheduler: a.scheduler, notifications: map[uint64]func(time.Time) bool{}}
	a.scheduler = fake
	a.web.Start()
	defer func() {
		if err := a.shutdown(); err != nil {
			t.Error(err)
		}
	}()
	if err := a.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	initial := a.Snapshot().Generation
	a.scheduler.Start()
	fake.failPrepare = true
	fake.failCleanup = true
	publish("candidate")
	if err := a.Reload(context.Background()); !errors.Is(err, errTestRegistration) {
		t.Fatal("registration failure was lost", err)
	}
	if a.Snapshot().Generation != initial || len(a.orphans) == 0 {
		t.Fatal("failed candidate changed working snapshot or lost cleanup ownership")
	}
	aborted := initial + 1
	if fake.notifications[aborted](time.Now().Add(24 * time.Hour)) {
		t.Fatal("aborted candidate callback was admitted")
	}
	fake.failPrepare = false
	// Retry identical bytes: a failed prepare must never acknowledge their digest.
	if err := a.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	current := a.Snapshot().Generation
	if current <= aborted {
		t.Fatal("aborted generation ID was reused")
	}
	if a.Snapshot().Schedules[0].Name != "candidate" {
		t.Fatal("unchanged failed bundle was not retried")
	}
	if fake.notifications[initial](time.Now().Add(24*time.Hour)) || fake.notifications[aborted](time.Now().Add(24*time.Hour)) {
		t.Fatal("orphan callback became active")
	}
	fake.failCleanup = false
	if err := a.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(a.orphans) != 0 || a.Snapshot().Generation != current {
		t.Fatal("cleanup retry changed or lost current generation")
	}
}

func TestReloadRetainsTheRuntimeTimezoneInstance(t *testing.T) {
	cfg := testConfig(t)
	cfg.Timezone = "Europe/Moscow"
	path := filepath.Join(cfg.SchedulesDir, "schedule.yaml")
	if err := os.WriteFile(path, []byte(testManifest), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New(cfg, &blockingCloud{}, Options{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	a.web.Start()
	defer func() {
		if err := a.shutdown(); err != nil {
			t.Error(err)
		}
	}()
	if err := a.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	initial := a.Snapshot().Location
	if initial != a.location {
		t.Fatal("runtime consumers do not share a timezone instance")
	}
	if err := os.WriteFile(path, []byte(strings.Replace(testManifest, "name: test", "name: updated", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.Snapshot().Location != initial {
		t.Fatal("hot reload reloaded timezone rules")
	}
}
