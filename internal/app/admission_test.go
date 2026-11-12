package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sentoz/vk-scheduler/internal/config"
	"github.com/sentoz/vk-scheduler/internal/executor"
)

type gatedCloud struct {
	blockingCloud
	preparing chan struct{}
	release   chan struct{}
	results   chan error
	posts     chan string
	calls     atomic.Int32
}

func (c *gatedCloud) Apply(ctx context.Context, _ config.Resource, action string, authorize func(context.Context) error) (bool, error) {
	if c.calls.Add(1) == 1 {
		close(c.preparing)
		select {
		case <-c.release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	err := authorize(ctx)
	if err == nil {
		c.posts <- action
	}
	c.results <- err
	return err == nil, err
}

func TestReservedWorkLosesPermissionAcrossLifecycleChanges(t *testing.T) {
	for _, scenario := range []string{"reload", "suspend", "remove", "shutdown"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.ValidationResources = false
			path := filepath.Join(cfg.SchedulesDir, "schedule.yaml")
			publish := func(text string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			publish(testManifest)
			cloud := &gatedCloud{preparing: make(chan struct{}), release: make(chan struct{}), results: make(chan error, 2), posts: make(chan string, 2)}
			a, err := New(cfg, cloud, Options{Address: "127.0.0.1:0"})
			if err != nil {
				t.Fatal(err)
			}
			a.web.Start()
			released := false
			defer func() {
				a.executor.CloseAdmission()
				if !released {
					close(cloud.release)
				}
				if err := a.shutdown(); err != nil {
					t.Error(err)
				}
			}()
			if err := a.Reload(context.Background()); err != nil {
				t.Fatal(err)
			}
			snapshot := a.Snapshot()
			var key string
			for k := range snapshot.Groups {
				key = k
			}
			if !a.executor.NotifyCorrection(snapshot.Generation, key) {
				t.Fatal("initial intent not accepted")
			}
			select {
			case <-cloud.preparing:
			case <-time.After(2 * time.Second):
				t.Fatal("worker did not reserve resource")
			}
			want := executor.ErrResourceRemoved
			switch scenario {
			case "reload":
				publish(strings.Replace(testManifest, "name: test", "name: renamed", 1))
				want = executor.ErrSuperseded
			case "suspend":
				maintenance := strings.Replace(testManifest, "name: test", "name: maintenance", 1)
				maintenance = strings.Replace(maintenance, "spec:\n", "spec:\n  suspend: true\n", 1)
				publish(testManifest + "---\n" + maintenance)
			case "remove":
				publish("")
			case "shutdown":
				a.executor.CloseAdmission()
				want = executor.ErrClosed
			}
			if scenario != "shutdown" {
				if err := a.Reload(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "suspend" && !a.Snapshot().Groups[key].Suspended {
				t.Fatal("one suspended manifest did not suspend the entire group")
			}
			close(cloud.release)
			released = true
			select {
			case err := <-cloud.results:
				if !errors.Is(err, want) {
					t.Fatalf("old lease permission: %v, want %v", err, want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("authorization did not finish")
			}
			if scenario == "reload" {
				select {
				case err := <-cloud.results:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("surviving intent was lost")
				}
				if action := <-cloud.posts; action != "start" {
					t.Fatal(action)
				}
			}
			if len(cloud.posts) != 0 {
				t.Fatal("stale reserved work mutated a resource")
			}
		})
	}
}
