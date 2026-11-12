package schedule

import (
	"context"
	"testing"
	"time"
)

func TestPreviousAcrossLocalMidnightJump(t *testing.T) {
	zone, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2018, 11, 4, 12, 0, 0, 0, zone)
	action, err := CompileAction("daily", "23:30", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	previous, err := action.Previous(ctx, now, zone)
	want := time.Date(2018, 11, 3, 23, 30, 0, 0, zone)
	if err != nil || !previous.Equal(want) {
		t.Fatalf("got %v, want %v, err=%v", previous, want, err)
	}
	start := DayStart(time.Date(2018, 11, 4, 0, 0, 0, 0, time.UTC), zone)
	if start.Format("2006-01-02 15:04") != "2018-11-04 01:00" {
		t.Fatal(start)
	}
}
