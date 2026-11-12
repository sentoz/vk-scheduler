package vkcloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func writeManagedCluster(t *testing.T, w http.ResponseWriter, state string) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(map[string]string{"id": testID, "project_id": testProject, "status": state}); err != nil {
		t.Error(err)
	}
}

func TestManagedK8sStartStopWireAndPolling(t *testing.T) {
	for _, action := range []string{"start", "stop"} {
		for _, code := range []int{200, 202, 204} {
			t.Run(fmt.Sprintf("%s/%d", action, code), func(t *testing.T) {
				target := testResource("k8s_v2")
				target.ID = "3AbCdE4FgHiJ5KlMnOpQrStUvWx"
				old, next, transition, command := "SHUTOFF", "RUNNING", "TURNING_ON", "turn_on"
				if action == "stop" {
					old, next, transition, command = "RUNNING", "SHUTOFF", "TURNING_OFF", "turn_off"
				}
				var posts, polls, permits atomic.Int32
				client := apiClient(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.Header.Get("MCS-API-Version") != "" {
						t.Error("Magnum microversion leaked to Managed-k8s")
					}
					if r.Method == http.MethodPost {
						if r.URL.Path != "/clusters/"+target.ID+"/"+command {
							t.Error("incorrect Managed-k8s command", r.URL.Path)
						}
						var body map[string]any
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body == nil || len(body) != 0 {
							t.Error("expected empty JSON object", body, err)
						}
						if r.Header.Get("Content-Type") != "application/json" {
							t.Error("missing JSON content type")
						}
						posts.Add(1)
						w.WriteHeader(code)
						return
					}
					if r.Method != http.MethodGet || r.URL.Path != "/clusters/"+target.ID {
						t.Error("unexpected request", r.Method, r.URL.Path)
					}
					state := old
					if posts.Load() != 0 {
						switch polls.Add(1) {
						case 1, 2:
						case 3:
							state = transition
						default:
							state = next
						}
					}
					if err := json.NewEncoder(w).Encode(map[string]string{"id": target.ID, "project_id": testProject, "status": state}); err != nil {
						t.Error(err)
					}
				}, "k8s_v2")
				changed, err := client.Apply(t.Context(), target, action, func(context.Context) error { permits.Add(1); return nil })
				if err != nil || !changed || posts.Load() != 1 || polls.Load() != 4 || permits.Load() != 1 || len(client.PendingOperations()) != 0 {
					t.Fatal(changed, err, posts.Load(), polls.Load(), permits.Load(), client.PendingOperations())
				}
			})
		}
	}
}

func TestManagedK8sPreconditionsNeverMutate(t *testing.T) {
	valid := `{"id":"` + testID + `","project_id":"` + testProject + `","status":"SHUTOFF"}`
	for _, item := range []struct {
		name string
		code int
		body string
		want error
	}{
		{"forbidden", 403, `{}`, ErrPermissionDenied},
		{"missing", 404, `{}`, ErrResourceNotFound},
		{"malformed", 200, `{`, ErrUnexpectedResponse},
		{"wrong ID", 200, `{"id":"other","project_id":"` + testProject + `","status":"SHUTOFF"}`, ErrUnexpectedResponse},
		{"Magnum UUID is not ID", 200, `{"uuid":"` + testID + `","project_id":"` + testProject + `","status":"SHUTOFF"}`, ErrUnexpectedResponse},
		{"missing ownership", 200, `{"id":"` + testID + `","status":"SHUTOFF"}`, ErrUnexpectedResponse},
		{"scope", 200, `{"id":"` + testID + `","project_id":"other","status":"SHUTOFF"}`, ErrScopeMismatch},
		{"wrapped DTO", 200, `{"cluster":` + valid + `}`, ErrUnexpectedResponse},
		{"failed", 200, `{"id":"` + testID + `","project_id":"` + testProject + `","status":"FAILED"}`, ErrResourceFailed},
		{"deleted", 200, `{"id":"` + testID + `","project_id":"` + testProject + `","status":"DELETED"}`, ErrResourceNotFound},
		{"unknown", 200, `{"id":"` + testID + `","project_id":"` + testProject + `","status":"STATUS_UNSPECIFIED"}`, ErrUnexpectedState},
	} {
		t.Run(item.name, func(t *testing.T) {
			client := apiClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("mutation without validated precondition")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(item.code)
				if _, err := w.Write([]byte(item.body)); err != nil {
					t.Error(err)
				}
			}, "k8s_v2")
			_, err := client.Apply(t.Context(), testResource("k8s_v2"), "start", func(context.Context) error { t.Error("invalid mutation authorized"); return nil })
			if !errors.Is(err, item.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestManagedK8sStateMapping(t *testing.T) {
	for _, status := range []string{"PROVISIONING", "STARTING", "RECONCILING", "DELETING", "TURNING_ON", "TURNING_OFF"} {
		state, transition, err := managedK8sState(status)
		if state != status || !transition || err != nil {
			t.Fatal(status, state, transition, err)
		}
	}
	for _, status := range []string{"", "STATUS_UNSPECIFIED", "PAUSED", "RUNNING_UNKNOWN"} {
		if _, _, err := managedK8sState(status); !errors.Is(err, ErrUnexpectedState) {
			t.Fatal(status, err)
		}
	}
}

func TestManagedK8sUnknownOutcomeAndRecovery(t *testing.T) {
	var posts atomic.Int32
	var state atomic.Value
	state.Store("SHUTOFF")
	client := apiClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(503)
			return
		}
		writeManagedCluster(t, w, state.Load().(string))
	}, "k8s_v2")
	target := testResource("k8s_v2")
	permit := func(context.Context) error { return nil }
	if _, err := client.Apply(t.Context(), target, "start", permit); !errors.Is(err, ErrUnconfirmedOperation) {
		t.Fatal(err)
	}
	if _, err := client.Apply(t.Context(), target, "stop", permit); !errors.Is(err, ErrUnconfirmedOperation) || posts.Load() != 1 {
		t.Fatal("opposite action bypassed uncertain attempt", err, posts.Load())
	}
	state.Store("TURNING_ON")
	if _, err := client.ObservePending(t.Context(), target); !errors.Is(err, ErrUnconfirmedOperation) {
		t.Fatal(err)
	}
	state.Store("RUNNING")
	if ok, err := client.ObservePending(t.Context(), target); !ok || err != nil || client.Unconfirmed(target.Key()) {
		t.Fatal(ok, err)
	}
	if changed, err := client.Apply(t.Context(), target, "start", permit); err != nil || changed || posts.Load() != 1 {
		t.Fatal("already-desired replayed a command", changed, err)
	}
}

func TestManagedK8sPollingCancellationKeepsMarker(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var posts atomic.Int32
	client := apiClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(200)
			return
		}
		if posts.Load() > 0 {
			cancel()
		}
		writeManagedCluster(t, w, "SHUTOFF")
	}, "k8s_v2")
	client.operationTimeout = time.Minute
	_, err := client.Apply(ctx, testResource("k8s_v2"), "start", func(context.Context) error { return nil })
	if !errors.Is(err, context.Canceled) || posts.Load() != 1 || len(client.PendingOperations()) != 1 {
		t.Fatal(err, posts.Load(), client.PendingOperations())
	}
}

func TestManagedK8sRejectedOrLostCommandIsNeverReplayed(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 409, 429, 500, 503, 0} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var posts atomic.Int32
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
				writeManagedCluster(t, w, "SHUTOFF")
			}, "k8s_v2")
			client.cloud.RequestTimeout.Duration = 100 * time.Millisecond
			_, err := client.Apply(t.Context(), testResource("k8s_v2"), "start", func(context.Context) error { return nil })
			if err == nil || posts.Load() != 1 {
				t.Fatal("failed command was replayed or reported success", err, posts.Load())
			}
			unknown := code == 0 || code >= 500
			if client.Unconfirmed(testResource("k8s_v2").Key()) != unknown {
				t.Fatal("incorrect unknown-outcome classification", code, err)
			}
		})
	}
}
