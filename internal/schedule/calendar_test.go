package schedule

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPreviousOccurrence(t *testing.T) {
	location, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	day31 := 31
	cases := []struct {
		name       string
		kind       string
		clock      string
		expression string
		day        *int
		now        string
		want       string
	}{
		{"minute cron", "cron", "", "* * * * *", nil,
			"2026-11-02T12:34:45+03:00", "2026-11-02T12:34:00+03:00"},
		{"inclusive daily", "daily", "09:00", "", nil,
			"2026-11-02T09:00:00+03:00", "2026-11-02T09:00:00+03:00"},
		{"monthly skips February", "monthly", "09:00", "", &day31,
			"2026-03-01T12:00:00+03:00", "2026-01-31T09:00:00+03:00"},
		{"daily seconds", "daily", "09:00:30", "", nil,
			"2026-11-02T09:00:29+03:00", "2026-11-01T09:00:30+03:00"},
		{"leap day", "cron", "", "0 9 29 2 *", nil,
			"2026-03-01T12:00:00+03:00", "2024-02-29T09:00:00+03:00"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			compiled, err := CompileAction(test.kind, test.clock, test.expression, test.day)
			if err != nil {
				t.Fatal(err)
			}
			now, err := time.Parse(time.RFC3339, test.now)
			if err != nil {
				t.Fatal(err)
			}
			want, err := time.Parse(time.RFC3339, test.want)
			if err != nil {
				t.Fatal(err)
			}
			got, err := compiled.Previous(context.Background(), now, location)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(want) {
				t.Fatalf("got %s, want %s", got.Format(time.RFC3339), test.want)
			}
		})
	}
}

func TestNextAndPreviousAgreeAcrossDST(t *testing.T) {
	zone, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := CompileAction("daily", "02:30", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		date  string
		count int
	}{{"2026-03-29", 0}, {"2026-10-25", 2}} {
		t.Run(item.date, func(t *testing.T) {
			date, err := time.Parse(time.DateOnly, item.date)
			if err != nil {
				t.Fatal(err)
			}
			start, end := DayStart(date, zone), DayStart(date.AddDate(0, 0, 1), zone)
			var occurrences []time.Time
			for next := compiled.Next(start.Add(-time.Second), zone); !next.IsZero() && next.Before(end); next = compiled.Next(next, zone) {
				occurrences = append(occurrences, next)
			}
			if len(occurrences) != item.count {
				t.Fatal("pinned parser DST semantics changed", occurrences)
			}
			for _, at := range occurrences {
				for _, now := range []time.Time{at, at.Add(20 * time.Minute)} {
					previous, err := compiled.Previous(context.Background(), now, zone)
					if err != nil || !previous.Equal(at) {
						t.Fatalf("next/previous disagree: previous=%v at=%v error=%v", previous, at, err)
					}
				}
			}
		})
	}
}

func TestNoPreviousOccurrenceDoesNotInventATarget(t *testing.T) {
	compiled, err := CompileAction("cron", "", "0 0 31 2 *", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := compiled.Previous(ctx, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), time.UTC); !errors.Is(err, ErrNoPreviousOccurrence) {
		t.Fatal("impossible calendar invented an occurrence", err)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := compiled.Previous(cancelled, time.Now(), time.UTC); !errors.Is(err, context.Canceled) {
		t.Fatal("previous ignored cancellation", err)
	}
}

func TestCompileRejectsUnsupportedCronLanguage(t *testing.T) {
	for _, expression := range []string{"* * * * * *", "@daily", "TZ=UTC 0 9 * * *", "CRON_TZ=UTC 0 9 * * *", "bad bad bad bad bad"} {
		if _, err := CompileAction("cron", "", expression, nil); !errors.Is(err, ErrInvalidCron) {
			t.Fatal("unsupported cron accepted", expression, err)
		}
	}
}
