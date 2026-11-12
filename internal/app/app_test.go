package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sentoz/vk-scheduler/internal/config"
	"github.com/sentoz/vk-scheduler/internal/vkcloud"
)

const testManifest = "apiVersion: scheduler.vk/v1alpha1\nkind: Schedule\nmetadata:\n  name: test\nspec:\n  type: cron\n  resource:\n    type: vm\n    id: 11111111-1111-4111-8111-111111111111\n  actions:\n    start:\n      enabled: true\n      crontab: '* * * * *'\n"

type blockingCloud struct {
	entered  chan struct{}
	finished chan struct{}
	closed   atomic.Int32
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{Timezone: "UTC", SchedulesDir: t.TempDir(), ValidationResources: true, ValidationInterval: config.Duration{Duration: time.Hour}, ShutdownTimeout: config.Duration{Duration: 500 * time.Millisecond}, OperationTimeout: config.Duration{Duration: time.Minute}, MaxConcurrentJobs: 1, MetricsPort: 9090, Cloud: config.CloudConfig{AuthURL: "https://identity.example.test/v3/", ProjectID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Region: "test-region", UserDomainName: "users", Interface: "public", RequestTimeout: config.Duration{Duration: time.Second}, PollInterval: config.Duration{Duration: time.Second}, MaxConcurrentReads: 2, MagnumMicroversion: "1.33"}}
}

func (c *blockingCloud) GetState(context.Context, config.Resource) (string, bool, error) {
	return "stopped", false, nil
}
func (c *blockingCloud) PrepareServices([]config.Resource) error       { return nil }
func (c *blockingCloud) PendingOperations() []vkcloud.PendingOperation { return nil }
func (c *blockingCloud) ObservePending(context.Context, config.Resource) (bool, error) {
	return true, nil
}
func (c *blockingCloud) Shutdown(context.Context) error { c.closed.Add(1); return nil }
func (c *blockingCloud) Apply(ctx context.Context, _ config.Resource, _ string, authorize func(context.Context) error) (bool, error) {
	if err := authorize(ctx); err != nil {
		return false, err
	}
	close(c.entered)
	<-ctx.Done()
	close(c.finished)
	return true, ctx.Err()
}

func TestAppCancelsAdmittedWorkBeforeHardDeadline(t *testing.T) {
	dir := t.TempDir()
	schedules := filepath.Join(dir, "schedules")
	if err := os.Mkdir(schedules, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(schedules, "test.yaml"), []byte(testManifest), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	cfg.SchedulesDir = schedules
	cloud := &blockingCloud{entered: make(chan struct{}), finished: make(chan struct{})}
	a, err := New(cfg, cloud, Options{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	select {
	case <-cloud.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("drain timeout was not reported", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not join")
	}
	select {
	case <-cloud.finished:
	default:
		t.Fatal("worker not joined")
	}
	if cloud.closed.Load() != 1 {
		t.Fatal("cloud shutdown not owned once")
	}
	if err := a.Run(context.Background()); !errors.Is(err, ErrAlreadyRun) {
		t.Fatal("App restarted")
	}
	if response, err := http.Get("http://" + a.Address() + "/health/live"); err == nil {
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
		t.Fatal("HTTP listener still alive")
	}
}

type slowShutdownCloud struct {
	blockingCloud
	closing chan struct{}
	release chan struct{}
	done    chan struct{}
}

func (c *slowShutdownCloud) Shutdown(context.Context) error {
	close(c.closing)
	<-c.release
	close(c.done)
	return nil
}

func TestHardDeadlineBoundsUncooperativeCloudShutdown(t *testing.T) {
	cfg := testConfig(t)
	cfg.ShutdownTimeout = config.Duration{Duration: 100 * time.Millisecond}
	cloud := &slowShutdownCloud{closing: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
	a, err := New(cfg, cloud, Options{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	a.web.Start()
	result := make(chan error, 1)
	go func() { result <- a.shutdown() }()
	select {
	case <-cloud.closing:
	case <-time.After(2 * time.Second):
		t.Fatal("cloud shutdown not called")
	}
	defer func() { close(cloud.release); <-cloud.done }()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("hard deadline was not reported: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("uncooperative shutdown escaped hard deadline")
	}
}

type startupRecoveryCloud struct {
	blockingCloud
	reads    atomic.Int32
	scanning chan struct{}
	observed chan struct{}
}

func (c *startupRecoveryCloud) GetState(ctx context.Context, _ config.Resource) (string, bool, error) {
	if c.reads.Add(1) > 1 {
		close(c.scanning)
		<-ctx.Done()
		return "", false, ctx.Err()
	}
	return "stopped", false, nil
}

func (c *startupRecoveryCloud) PendingOperations() []vkcloud.PendingOperation {
	return []vkcloud.PendingOperation{{Resource: config.Resource{Type: "vm", ID: "11111111-1111-4111-8111-111111111111", ProjectID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Region: "test-region"}, Desired: "running"}}
}

func (c *startupRecoveryCloud) ObservePending(context.Context, config.Resource) (bool, error) {
	select {
	case c.observed <- struct{}{}:
	default:
	}
	return false, vkcloud.ErrUnconfirmedOperation
}

func TestRecoveryStartsBeforeInitialValidatorScanFinishes(t *testing.T) {
	cfg := testConfig(t)
	if err := os.WriteFile(filepath.Join(cfg.SchedulesDir, "schedule.yaml"), []byte(testManifest), 0600); err != nil {
		t.Fatal(err)
	}
	cloud := &startupRecoveryCloud{scanning: make(chan struct{}), observed: make(chan struct{}, 1)}
	a, err := New(cfg, cloud, Options{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("startup cancellation did not join")
		}
	}()
	select {
	case <-cloud.scanning:
	case <-time.After(2 * time.Second):
		t.Fatal("initial validator scan not reached")
	}
	select {
	case <-cloud.observed:
	case <-time.After(2 * time.Second):
		t.Fatal("recovery waited for initial validator scan")
	}
}
