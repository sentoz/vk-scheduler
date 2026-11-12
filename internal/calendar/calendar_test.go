package calendar

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sentoz/vk-scheduler/internal/config"
)

func TestCalendarRangesAndOverflow(t *testing.T) {
	zone, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"2026-01-01", ""}, {"", "2026-01-01"}, {"2026-02-30", "2026-03-01"}, {"2026-03-02", "2026-03-01"}, {"2026-01-01", "2027-01-02"}} {
		if _, _, err := Range(pair[0], pair[1], time.Now(), zone); !errors.Is(err, ErrInvalidRange) {
			t.Fatal(pair, err)
		}
	}
	start, end, err := Range("2026-03-29", "2026-03-29", time.Now(), zone)
	if err != nil || end.Sub(start) != 23*time.Hour {
		t.Fatalf("DST range %v", err)
	}
	snapshot, err := config.CompileSnapshot(1, []config.Schedule{{Name: "every-minute", Type: "cron", Resource: config.Resource{Type: "vm", ID: "example"}, Actions: config.Actions{Start: &config.ActionConfig{Enabled: true, Crontab: "* * * * *"}}}}, zone)
	if err != nil {
		t.Fatal(err)
	}
	events, err := Events(context.Background(), snapshot, start, end)
	if err != nil || len(events) != 23*60 {
		t.Fatalf("DST count=%d err=%v", len(events), err)
	}
	_, err = Events(context.Background(), snapshot, start, start.AddDate(0, 0, 100))
	if !errors.Is(err, ErrEventLimit) {
		t.Fatal(err)
	}
}

func TestCalendarBoundsResponseBytesForLargeMetadata(t *testing.T) {
	start := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	snapshot, err := config.CompileSnapshot(1, []config.Schedule{{Name: "frequent", DisplayName: strings.Repeat("x", 128<<10), Type: "cron", Resource: config.Resource{Type: "vm", ID: "test"}, Actions: config.Actions{Start: &config.ActionConfig{Enabled: true, Crontab: "* * * * *"}}}}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Events(context.Background(), snapshot, start, start.AddDate(0, 0, 1)); !errors.Is(err, ErrResponseLimit) {
		t.Fatalf("large metadata bypassed response budget: %v", err)
	}
}
