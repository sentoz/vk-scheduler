package vkcloud

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestNovaStartStopCycleUsesJSONNull(t *testing.T) {
	var running atomic.Bool
	var posts atomic.Int32
	client := apiClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body) != 1 {
				t.Error("unexpected body fields")
			}
			if value, ok := body["os-start"]; ok {
				if string(value) != "null" {
					t.Error("start body is not JSON null")
				}
				running.Store(true)
			} else {
				if string(body["os-stop"]) != "null" {
					t.Error("stop body is not JSON null")
				}
				running.Store(false)
			}
			posts.Add(1)
			w.WriteHeader(202)
			return
		}
		state := "SHUTOFF"
		if running.Load() {
			state = "ACTIVE"
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"server": map[string]string{"id": testID, "tenant_id": testProject, "status": state}}); err != nil {
			t.Error(err)
		}
	}, "vm")
	for _, action := range []string{"start", "stop"} {
		if changed, err := client.Apply(context.Background(), testResource("vm"), action, func(context.Context) error { return nil }); err != nil || !changed {
			t.Fatal(action, changed, err)
		}
	}
	if posts.Load() != 2 || len(client.PendingOperations()) != 0 {
		t.Fatal("unexpected cycle lifecycle")
	}
}

func TestActionFailuresNeverReplayPOST(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 409, 429, 500, 503, 0} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var reads, posts atomic.Int32
			client := apiClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					posts.Add(1)
					if code == 0 {
						<-r.Context().Done()
						return
					}
					w.WriteHeader(code)
					return
				}
				reads.Add(1)
				if err := json.NewEncoder(w).Encode(map[string]any{"server": map[string]string{"id": testID, "tenant_id": testProject, "status": "SHUTOFF"}}); err != nil {
					t.Error(err)
				}
			}, "vm")
			if code == 0 {
				client.cloud.RequestTimeout.Duration = 200 * time.Millisecond
			}
			if _, err := client.Apply(context.Background(), testResource("vm"), "start", func(context.Context) error { return nil }); err == nil {
				t.Fatal("failed action reported success")
			}
			if posts.Load() != 1 {
				t.Fatal("POST replayed")
			}
			wantReads := int32(1)
			if code == 409 {
				wantReads = 2
			}
			if reads.Load() != wantReads {
				t.Fatal("unexpected diagnostic or replay GET", reads.Load())
			}
			pending := len(client.PendingOperations())
			if (code == 0 || code >= 500) && pending != 1 {
				t.Fatal("unknown outcome lost its marker")
			}
			if code >= 400 && code < 500 && pending != 0 {
				t.Fatal("known rejection kept an unknown marker")
			}
		})
	}
}

func TestPostAcceptanceTerminalReadFailureIsNotSuccess(t *testing.T) {
	for _, status := range []string{"ERROR", "DELETED", "SHUTOFF"} {
		t.Run(status, func(t *testing.T) {
			var posts atomic.Int32
			client := apiClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost {
					posts.Add(1)
					w.WriteHeader(202)
					return
				}
				state := "SHUTOFF"
				if posts.Load() > 0 {
					state = status
				}
				if state == "DELETED" {
					w.WriteHeader(404)
					return
				}
				if err := json.NewEncoder(w).Encode(map[string]any{"server": map[string]string{"id": testID, "tenant_id": testProject, "status": state}}); err != nil {
					t.Error(err)
				}
			}, "vm")
			client.operationTimeout = 200 * time.Millisecond
			_, err := client.Apply(context.Background(), testResource("vm"), "start", func(context.Context) error { return nil })
			want := ErrResourceFailed
			if status == "DELETED" {
				want = ErrResourceNotFound
			}
			if status == "SHUTOFF" {
				want = ErrOperationTimeout
			}
			if !errors.Is(err, want) || !errors.Is(err, ErrUnconfirmedOperation) || posts.Load() != 1 || len(client.PendingOperations()) != 1 {
				t.Fatal("invalid acceptance lifecycle", err)
			}
		})
	}
}

func TestTransitionalPreconditionAndPostPollingHonorCancellation(t *testing.T) {
	for _, afterPost := range []bool{false, true} {
		t.Run(map[bool]string{false: "precondition", true: "polling"}[afterPost], func(t *testing.T) {
			entered := make(chan struct{}, 1)
			var posts atomic.Int32
			client := apiClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost {
					posts.Add(1)
					w.WriteHeader(202)
					return
				}
				state, task := "ACTIVE", "powering-on"
				if afterPost && posts.Load() == 0 {
					state, task = "SHUTOFF", ""
				} else {
					select {
					case entered <- struct{}{}:
					default:
					}
				}
				if err := json.NewEncoder(w).Encode(map[string]any{"server": map[string]string{"id": testID, "tenant_id": testProject, "status": state, "OS-EXT-STS:task_state": task}}); err != nil {
					t.Error(err)
				}
			}, "vm")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := client.Apply(ctx, testResource("vm"), "start", func(context.Context) error { return nil })
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("transition not reached")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("polling ignored cancellation")
			}
			if afterPost {
				if posts.Load() != 1 || len(client.PendingOperations()) != 1 {
					t.Fatal("accepted attempt was not retained")
				}
			} else if posts.Load() != 0 || len(client.PendingOperations()) != 0 {
				t.Fatal("transitional precondition mutated resource")
			}
		})
	}
}
