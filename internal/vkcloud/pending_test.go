package vkcloud

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/sentoz/vk-scheduler/internal/config"
)

func TestUnconfirmedOperationBlockedWithinProcess(t *testing.T) {
	const project = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const id = "11111111-1111-4111-8111-111111111111"
	var running atomic.Bool
	var starts, stops atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			state := "SHUTOFF"
			if running.Load() {
				state = "ACTIVE"
			}
			if err := json.NewEncoder(w).Encode(map[string]any{"server": map[string]any{
				"id": id, "tenant_id": project, "status": state,
			}}); err != nil {
				t.Error(err)
			}
			return
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if _, exists := body["os-start"]; exists {
			starts.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if _, exists := body["os-stop"]; !exists {
			t.Error("unexpected action")
			w.WriteHeader(400)
			return
		}
		stops.Add(1)
		running.Store(false)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer api.Close()
	provider, err := openstack.NewClient(api.URL + "/identity/v3/")
	if err != nil {
		t.Fatal(err)
	}
	provider.HTTPClient = *api.Client()
	client := &Client{provider: provider,
		compute: serviceClient(provider, api.URL+"/compute/", "compute", ""),
		cloud: config.CloudConfig{ProjectID: project, Region: "test-region",
			RequestTimeout: config.Duration{Duration: time.Second}, PollInterval: config.Duration{Duration: time.Millisecond}},
		operationTimeout: time.Second}
	target := config.Resource{Type: "vm", ID: id, ProjectID: project, Region: "test-region"}
	permit := func(context.Context) error { return nil }
	if _, err := client.Apply(context.Background(), target, "start", permit); !errors.Is(err, ErrUnconfirmedOperation) {
		t.Fatalf("expected unknown result, got %v", err)
	}
	if _, err := client.Apply(context.Background(), target, "stop", permit); !errors.Is(err, ErrUnconfirmedOperation) {
		t.Fatalf("old SHUTOFF incorrectly satisfied stop: %v", err)
	}
	if starts.Load() != 1 || stops.Load() != 0 {
		t.Fatal("mutation replayed while unconfirmed")
	}
	running.Store(true)
	if ok, err := client.ObservePending(context.Background(), target); !ok || err != nil {
		t.Fatal(err)
	}
	if _, err := client.Apply(context.Background(), target, "stop", permit); err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 1 || stops.Load() != 1 || len(client.operations.Records()) != 0 {
		t.Fatal("unexpected confirmed operation lifecycle")
	}
}

func TestRestartStartsWithEmptyTracker(t *testing.T) {
	target := config.Resource{Type: "vm", ID: "test", ProjectID: "project", Region: "region"}
	old := &Client{}
	if _, err := old.operations.Begin(target, "running"); err != nil {
		t.Fatal(err)
	}
	fresh := &Client{}
	if _, exists := fresh.operations.Pending(target.Key()); exists {
		t.Fatal("unexpected persistence")
	}
	if len(old.operations.Records()) != 1 {
		t.Fatal("old process marker changed")
	}
}

func TestStaleAttemptCannotClearNewerMarker(t *testing.T) {
	target := config.Resource{Type: "vm", ID: "test", ProjectID: "project", Region: "region"}
	var tracker pendingOperations
	first, err := tracker.Begin(target, "running")
	if err != nil {
		t.Fatal(err)
	}
	if err := tracker.Confirm(first); err != nil {
		t.Fatal(err)
	}
	second, err := tracker.Begin(target, "stopped")
	if err != nil {
		t.Fatal(err)
	}
	if err := tracker.Confirm(first); !errors.Is(err, ErrUnconfirmedOperation) {
		t.Fatal("stale confirmation accepted")
	}
	current, exists := tracker.Pending(target.Key())
	if !exists || current.Attempt != second.Attempt {
		t.Fatal("newer attempt removed")
	}
}

func TestReadBudgetBlocksAndCancelsExtraCaller(t *testing.T) {
	client := &Client{cloud: config.CloudConfig{MaxConcurrentReads: 2}}
	root, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan struct{}, 2)
	done := make(chan error, 2)
	var calls atomic.Int32
	transport := &readTransport{client: client, base: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	request, err := http.NewRequestWithContext(root, http.MethodGet, "https://example.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		go func() { _, err := transport.RoundTrip(request); done <- err }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-root.Done():
			t.Fatal(root.Err())
		}
	}
	extra, cancelExtra := context.WithCancel(root)
	cancelExtra()
	if _, err := transport.RoundTrip(request.WithContext(extra)); !errors.Is(err, context.Canceled) || calls.Load() != 2 {
		t.Fatal("cancelled extra GET entered transport", err)
	}
	cancel()
	for range 2 {
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal("active transport did not cancel", err)
		}
	}
	if len(client.readSlots) != 0 {
		t.Fatal("failed GETs leaked read budget")
	}
}
