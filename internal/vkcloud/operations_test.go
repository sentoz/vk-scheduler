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

const testProject = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const testID = "11111111-1111-4111-8111-111111111111"

func apiClient(t *testing.T, handler http.HandlerFunc, kind string) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	p, err := openstack.NewClient(server.URL + "/infra/identity/v3/")
	if err != nil {
		t.Fatal(err)
	}
	p.HTTPClient = *server.Client()
	c := &Client{provider: p, cloud: config.CloudConfig{ProjectID: testProject, Region: "test-region", MaxConcurrentReads: 2, RequestTimeout: config.Duration{Duration: time.Second}, PollInterval: config.Duration{Duration: time.Millisecond}}, operationTimeout: time.Second}
	switch kind {
	case "vm":
		c.compute = serviceClient(p, server.URL+"/", "compute", "")
	case "k8s_v2":
		c.managedK8s = serviceClient(p, server.URL+"/", "managed-k8s", "")
	default:
		c.magnum = serviceClient(p, server.URL+"/", "container-infra", "1.33")
	}
	return c
}
func testResource(kind string) config.Resource {
	return config.Resource{Type: kind, ID: testID, ProjectID: testProject, Region: "test-region"}
}

func TestMagnumStartStopWireAndPolling(t *testing.T) {
	for _, action := range []string{"start", "stop"} {
		t.Run(action, func(t *testing.T) {
			var posts atomic.Int32
			var reads atomic.Int32
			client := apiClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Header.Get("MCS-API-Version") != "container-infra 1.33" {
					t.Error("wrong version header")
				}
				if r.Method == http.MethodPost {
					if r.URL.Path != "/clusters/"+testID+"/actions/" {
						t.Error(r.URL.Path)
					}
					var body struct {
						Action  string         `json:"action"`
						Payload map[string]any `json:"payload"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					want := "turn_on_cluster"
					if action == "stop" {
						want = "turn_off_cluster"
					}
					if body.Action != want || body.Payload == nil || len(body.Payload) != 0 {
						t.Error("invalid action payload")
					}
					posts.Add(1)
					w.WriteHeader(202)
					if err := json.NewEncoder(w).Encode(map[string]string{"uuid": testID}); err != nil {
						t.Error(err)
					}
					return
				}
				state := "SHUTOFF"
				if action == "stop" {
					state = "RUNNING"
				}
				if posts.Load() > 0 && reads.Add(1) > 2 {
					state = "RUNNING"
					if action == "stop" {
						state = "SHUTOFF"
					}
				}
				if err := json.NewEncoder(w).Encode(map[string]string{"uuid": testID, "project_id": testProject, "new_status": state, "status": "UPDATE_COMPLETE"}); err != nil {
					t.Error(err)
				}
			}, "k8s_v1")
			changed, err := client.Apply(context.Background(), testResource("k8s_v1"), action, func(context.Context) error { return nil })
			if err != nil || !changed || posts.Load() != 1 {
				t.Fatal(changed, err, posts.Load())
			}
		})
	}
}

func TestPreconditionFailuresNeverMutate(t *testing.T) {
	for _, item := range []struct {
		name string
		code int
		body string
		want error
	}{
		{"forbidden", 403, `{}`, ErrPermissionDenied}, {"missing", 404, `{}`, ErrResourceNotFound}, {"malformed", 200, `{`, ErrUnexpectedResponse},
		{"scope", 200, `{"server":{"id":"` + testID + `","tenant_id":"other","status":"SHUTOFF"}}`, ErrScopeMismatch},
		{"identity", 200, `{"server":{"id":"other","tenant_id":"` + testProject + `","status":"SHUTOFF"}}`, ErrUnexpectedResponse},
		{"failed", 200, `{"server":{"id":"` + testID + `","tenant_id":"` + testProject + `","status":"ERROR","OS-EXT-STS:task_state":"task"}}`, ErrResourceFailed},
		{"paused", 200, `{"server":{"id":"` + testID + `","tenant_id":"` + testProject + `","status":"PAUSED"}}`, ErrUnsupportedState},
		{"unknown", 200, `{"server":{"id":"` + testID + `","tenant_id":"` + testProject + `","status":"NEW_UNKNOWN"}}`, ErrUnexpectedState},
	} {
		t.Run(item.name, func(t *testing.T) {
			client := apiClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("mutation despite invalid precondition")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(item.code)
				if _, err := w.Write([]byte(item.body)); err != nil {
					t.Error(err)
				}
			}, "vm")
			if _, err := client.Apply(context.Background(), testResource("vm"), "start", func(context.Context) error { t.Error("authorized invalid state"); return nil }); !errors.Is(err, item.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestAlreadyDesiredAndSupersessionDoNotPost(t *testing.T) {
	for _, state := range []string{"ACTIVE", "SHUTOFF"} {
		t.Run(state, func(t *testing.T) {
			client := apiClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("unexpected POST")
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{"server": map[string]string{"id": testID, "tenant_id": testProject, "status": state}}); err != nil {
					t.Error(err)
				}
			}, "vm")
			refusal := errors.New("superseded")
			changed, err := client.Apply(context.Background(), testResource("vm"), "start", func(context.Context) error { return refusal })
			if changed {
				t.Fatal("mutation reported")
			}
			if state == "ACTIVE" && err != nil {
				t.Fatal(err)
			}
			if state == "SHUTOFF" && !errors.Is(err, refusal) {
				t.Fatal(err)
			}
			if len(client.PendingOperations()) != 0 {
				t.Fatal("unsent marker retained")
			}
		})
	}
}

func TestRecoveryDoesNotClearAnActiveAttempt(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	client := apiClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
			}
			w.WriteHeader(503)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"server": map[string]string{"id": testID, "tenant_id": testProject, "status": "SHUTOFF"}}); err != nil {
			t.Error(err)
		}
	}, "vm")
	done := make(chan error, 1)
	go func() {
		_, err := client.Apply(context.Background(), testResource("vm"), "start", func(context.Context) error { return nil })
		done <- err
	}()
	<-entered
	if ok, err := client.ObservePending(context.Background(), testResource("vm")); ok || !errors.Is(err, ErrOperationConflict) {
		t.Fatal("observer raced active mutation", err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrUnconfirmedOperation) {
		t.Fatal(err)
	}
}
