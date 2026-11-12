package scheduler

import (
	"testing"
	"time"

	"github.com/sentoz/vk-scheduler/internal/config"
)

func TestTimersOnlyDeliverCapturedGeneration(t *testing.T) {
	s, err := New(time.UTC, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Shutdown(); err != nil {
			t.Error(err)
		}
	}()
	snapshot, err := config.CompileSnapshot(7, []config.Schedule{{Name: "test", Type: "daily", Resource: config.Resource{ID: "test"}, Actions: config.Actions{Start: &config.ActionConfig{Enabled: true, Time: "09:00"}}}}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan uint64, 1)
	ids, err := s.Prepare(snapshot, func(generation uint64, _ string, _ time.Time) bool { seen <- generation; return true })
	if err != nil {
		t.Fatal(err)
	}
	s.Start()
	if err := s.clock.Jobs()[0].RunNow(); err != nil {
		t.Fatal(err)
	}
	select {
	case generation := <-seen:
		if generation != 7 {
			t.Fatal(generation)
		}
	case <-time.After(time.Second):
		t.Fatal("notification not delivered")
	}
	if failed, err := s.Remove(ids); err != nil || len(failed) != 0 {
		t.Fatal(err)
	}
}
