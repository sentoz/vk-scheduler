package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sentoz/vk-scheduler/internal/config"
	"github.com/sentoz/vk-scheduler/internal/metrics"
	"github.com/sentoz/vk-scheduler/internal/vars"
)

type checkerFunc func(context.Context, config.Resource) (string, bool, error)

func (f checkerFunc) GetState(ctx context.Context, r config.Resource) (string, bool, error) {
	return f(ctx, r)
}

func TestCancelledStatusRequestDoesNotStartAFlight(t *testing.T) {
	root, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()
	cache := &statusCache{ctx: root, timeout: time.Second, entries: map[string]cacheEntry{}, flights: map[cacheKey]*flight{}, checker: checkerFunc(func(context.Context, config.Resource) (string, bool, error) {
		t.Error("cancelled waiter started a cloud GET")
		return "running", false, nil
	})}
	cancelled, cancel := context.WithCancel(root)
	cancel()
	if result := cache.get(cancelled, 1, config.Resource{ID: "test"}); result.state != "unknown" {
		t.Fatal(result)
	}
	cancelRoot()
	if result := cache.get(context.Background(), 1, config.Resource{ID: "test"}); result.state != "unknown" {
		t.Fatal(result)
	}
	cache.workers.Wait()
}

func TestOldFlightDoesNotPopulateNewGenerationCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	cache := &statusCache{ctx: ctx, timeout: time.Second, entries: map[string]cacheEntry{}, flights: map[cacheKey]*flight{}, checker: checkerFunc(func(context.Context, config.Resource) (string, bool, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			return "running", false, nil
		}
		return "stopped", false, nil
	})}
	target := config.Resource{ID: "test"}
	old := make(chan status, 1)
	go func() { old <- cache.get(ctx, 1, target) }()
	<-entered
	if current := cache.get(ctx, 2, target); current.state != "stopped" {
		t.Fatal(current)
	}
	close(release)
	if result := <-old; result.state != "running" {
		t.Fatal(result)
	}
	if current := cache.get(ctx, 2, target); current.state != "stopped" || calls.Load() != 2 {
		t.Fatal("old generation replaced current cache")
	}
	cache.workers.Wait()
}

func TestCalendarErrorsAreSafeAndRequestsAreReadonly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, err := metrics.New()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := config.CompileSnapshot(1, []config.Schedule{{Name: "test", DisplayName: "test", Type: "daily", Resource: config.Resource{Type: "vm", ID: "test", ProjectID: "project", Region: "region"}, Actions: config.Actions{Start: &config.ActionConfig{Enabled: true, Time: "09:00"}}}}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int32
	cfg := config.Config{UIEnabled: true, Cloud: config.CloudConfig{RequestTimeout: config.Duration{Duration: time.Second}}}
	s, err := New(ctx, "127.0.0.1:0", cfg, func() *config.Snapshot { return snapshot }, checkerFunc(func(context.Context, config.Resource) (string, bool, error) {
		reads.Add(1)
		return "", false, errors.New("secret-canary https://private.example.test/")
	}), m)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		if err := s.listener.Close(); err != nil {
			t.Error(err)
		}
		s.cache.workers.Wait()
	}()
	for _, item := range []struct {
		method, path string
		code         int
	}{{"GET", "/api/calendar?from=2026-11-02", 400}, {"POST", "/api/calendar", 405}, {"GET", "/api/calendar?from=2026-11-02&to=2026-11-02", 200}} {
		recorder := httptest.NewRecorder()
		s.server.Handler.ServeHTTP(recorder, httptest.NewRequest(item.method, item.path, nil))
		if recorder.Code != item.code {
			t.Fatal(item, recorder.Code)
		}
		if strings.Contains(recorder.Body.String(), "secret-canary") || strings.Contains(recorder.Body.String(), "private.example.test") {
			t.Fatal("cloud detail leaked to UI")
		}
	}
	if reads.Load() != 1 {
		t.Fatal("invalid or mutating requests queried cloud")
	}
}

func TestStatusCacheSharesReadsAndIsolatesWaiterCancellation(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	checker := checkerFunc(func(ctx context.Context, _ config.Resource) (string, bool, error) {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
			return "running", false, nil
		case <-ctx.Done():
			return "", false, ctx.Err()
		}
	})
	cache := &statusCache{ctx: root, timeout: time.Second, checker: checker, entries: map[string]cacheEntry{}, flights: map[cacheKey]*flight{}}
	target := config.Resource{ID: "test"}
	first, cancelFirst := context.WithCancel(root)
	one := make(chan status, 1)
	two := make(chan status, 1)
	go func() { one <- cache.get(first, 1, target) }()
	<-entered
	go func() { two <- cache.get(root, 1, target) }()
	cancelFirst()
	if got := <-one; got.state != "unknown" {
		t.Fatal(got)
	}
	close(release)
	if got := <-two; got.state != "running" {
		t.Fatal(got)
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate cold GET")
	}
	if got := cache.get(root, 2, target); got.state != "running" || calls.Load() != 2 {
		t.Fatal("old generation cache reused")
	}
	cache.workers.Wait()
}

func TestHTTPReadinessTogglesAndBindConflict(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, err := metrics.New()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Cloud: config.CloudConfig{RequestTimeout: config.Duration{Duration: time.Second}}}
	s, err := New(ctx, "127.0.0.1:0", cfg, func() *config.Snapshot { return nil }, checkerFunc(func(context.Context, config.Resource) (string, bool, error) {
		t.Fatal("probe queried cloud")
		return "", false, nil
	}), m)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate, err := New(ctx, s.Address(), cfg, nil, nil, m); err == nil {
		t.Fatal("bind conflict accepted", duplicate)
	}
	for _, item := range []struct {
		path  string
		ready bool
		code  int
	}{{"/health/ready", false, 503}, {"/health/ready", true, 200}, {"/health/live", false, 200}, {"/metrics", false, 404}, {"/api/calendar", false, 404}, {"/", false, 404}, {"/info", false, 200}} {
		s.SetReady(item.ready)
		r := httptest.NewRecorder()
		s.server.Handler.ServeHTTP(r, httptest.NewRequest(http.MethodGet, item.path, nil))
		if r.Code != item.code {
			t.Fatal(item, r.Code)
		}
	}
	s.Start()
	cancel()
	hard, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := s.Shutdown(hard); err != nil {
		t.Fatal(err)
	}
}

func TestRootUIAndBuildInfoRouting(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			m, err := metrics.New()
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.Config{UIEnabled: enabled, Cloud: config.CloudConfig{RequestTimeout: config.Duration{Duration: time.Second}}}
			s, err := New(t.Context(), "127.0.0.1:0", cfg, func() *config.Snapshot { return nil }, checkerFunc(func(context.Context, config.Resource) (string, bool, error) {
				t.Error("static route or build info queried cloud")
				return "", false, nil
			}), m)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := s.listener.Close(); err != nil {
					t.Error(err)
				}
			}()
			uiCode, calendarCode := 404, 404
			if enabled {
				uiCode, calendarCode = 200, 503
			}
			for _, item := range []struct {
				path, contentType, content string
				code                       int
			}{
				{"/", "text/html", `src="/app.js"`, uiCode},
				{"/app.js", "text/javascript", "/api/calendar", uiCode},
				{"/style.css", "text/css", ".app", uiCode},
				{"/api/calendar", "", "", calendarCode},
				{"/ui/", "", "", 404},
				{"/missing", "", "", 404},
				{"/info/", "", "", 404},
				{"/metrics", "", "", 404},
			} {
				r := httptest.NewRecorder()
				s.server.Handler.ServeHTTP(r, httptest.NewRequest(http.MethodGet, item.path, nil))
				if r.Code != item.code {
					t.Fatalf("%s: status %d, want %d", item.path, r.Code, item.code)
				}
				if item.code == 200 && (!strings.HasPrefix(r.Header().Get("Content-Type"), item.contentType) || !strings.Contains(r.Body.String(), item.content)) {
					t.Fatalf("%s: wrong static response: %s", item.path, r.Header().Get("Content-Type"))
				}
				if item.path == "/" && enabled && !strings.Contains(r.Body.String(), `href="/style.css"`) {
					t.Fatal("root UI stylesheet path was not updated")
				}
			}
			r := httptest.NewRecorder()
			s.server.Handler.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/info", nil))
			var info vars.BuildInfo
			if err := json.Unmarshal(r.Body.Bytes(), &info); err != nil {
				t.Fatal(err)
			}
			want := vars.Info()
			if r.Code != 200 || r.Header().Get("Content-Type") != "application/json" || info.Version != want.Version || info.Commit != want.Commit || info.URL != want.URL || !info.BuildTime.Equal(want.BuildTime) {
				t.Fatal("build metadata unavailable at /info", r.Code, info)
			}
			r = httptest.NewRecorder()
			s.server.Handler.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/info", nil))
			if r.Code != http.StatusMethodNotAllowed {
				t.Fatal("build metadata accepts mutations", r.Code)
			}
		})
	}
}
