package reloader

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestFailedReloadIsRetriedAndCancellationStopsCallbacks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	retried := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, time.Millisecond, func(ctx context.Context) error {
			if calls.Add(1) == 1 {
				return errors.New("synthetic reload failure")
			}
			close(retried)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	select {
	case <-retried:
	case <-time.After(2 * time.Second):
		t.Fatal("failed reload was not retried")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reloader ignored cancellation")
	}
	if calls.Load() != 2 {
		t.Fatal("callback started after cancellation")
	}
}
