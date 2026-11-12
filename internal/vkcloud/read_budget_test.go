package vkcloud

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sentoz/vk-scheduler/internal/config"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGETBudgetHoldsUntilBodyCloseAndDoesNotBlockAuth(t *testing.T) {
	var calls atomic.Int32
	c := &Client{cloud: config.CloudConfig{MaxConcurrentReads: 1}}
	transport := &readTransport{client: c, base: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("synthetic")), Request: r}, nil
	})}
	get, err := http.NewRequest(http.MethodGet, "https://example.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := transport.RoundTrip(get)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := transport.RoundTrip(get.WithContext(ctx)); err == nil || calls.Load() != 1 {
		t.Fatal("GET bypassed occupied budget")
	}
	post, err := http.NewRequest(http.MethodPost, "https://example.test/auth/tokens", nil)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := transport.RoundTrip(post)
	if err != nil {
		t.Fatal("auth blocked by GET budget")
	}
	if err := auth.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Body.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := transport.RoundTrip(get)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(second.Body); err != nil {
		t.Fatal(err)
	}
	if err := second.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if len(c.readSlots) != 0 {
		t.Fatal("body leaked a budget slot")
	}
}
