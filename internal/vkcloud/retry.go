package vkcloud

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"syscall"
	"time"

	"github.com/gophercloud/gophercloud/v2"
)

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryableRead(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, ErrUnexpectedResponse) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) {
		return true
	}
	var response gophercloud.ErrUnexpectedResponseCode
	if errors.As(err, &response) {
		switch response.Actual {
		case 429, 500, 502, 503, 504:
			return true
		}
		return false
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return true
	}
	var dns *net.DNSError
	if errors.As(err, &dns) && dns.IsTemporary {
		return true
	}
	var request *url.Error
	return errors.As(err, &request) && (errors.Is(request.Err, io.EOF) || errors.Is(request.Err, io.ErrUnexpectedEOF))
}

func readRetryDelay(err error, fallback time.Duration, now time.Time) time.Duration {
	var response gophercloud.ErrUnexpectedResponseCode
	if !errors.As(err, &response) || response.Actual != 429 {
		return fallback
	}
	raw := response.ResponseHeader.Get("Retry-After")
	if seconds, parseErr := strconv.ParseInt(raw, 10, 64); parseErr == nil && seconds >= 0 {
		if seconds > 5 {
			seconds = 5
		}
		return time.Duration(seconds) * time.Second
	}
	if instant, parseErr := http.ParseTime(raw); parseErr == nil {
		delay := instant.Sub(now)
		if delay < 0 {
			return 0
		}
		if delay > 5*time.Second {
			return 5 * time.Second
		}
		return delay
	}
	return fallback
}

func readWithRetry(ctx context.Context, read func(context.Context) error,
	pause func(context.Context, time.Duration) error) error {
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := read(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil || attempt == 2 || !retryableRead(err) {
			return err
		}
		delay := time.Duration(attempt+1) * time.Second
		delay = readRetryDelay(err, delay, time.Now())
		if err := pause(ctx, delay); err != nil {
			return err
		}
	}
	return ErrUnexpectedResponse
}
