package vkcloud

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"context"

	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/sentoz/vk-scheduler/internal/config"
)

func TestStartInstanceHTTPContract(t *testing.T) {
	const projectID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const instanceID = "11111111-1111-4111-8111-111111111111"
	var posts atomic.Int32
	var polls atomic.Int32
	var changed atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Auth-Token") != "synthetic-token" {
			t.Error("missing synthetic authentication header")
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2.1/"+projectID+"/servers/"+instanceID:
			status, task := "SHUTOFF", ""
			if changed.Load() {
				status = "ACTIVE"
				if polls.Add(1) == 1 {
					task = "powering-on"
				}
			}
			if err := json.NewEncoder(w).Encode(map[string]any{
				"server": map[string]any{
					"id": instanceID, "tenant_id": projectID, "status": status,
					"OS-EXT-STS:task_state": task,
				},
			}); err != nil {
				t.Errorf("encode response: %v", err)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/v2.1/"+projectID+"/servers/"+instanceID+"/action":
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode action: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if len(body) != 1 || string(body["os-start"]) != "null" {
				t.Errorf("unexpected action body: %s", body["os-start"])
			}
			posts.Add(1)
			changed.Store(true)
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	provider, err := openstack.NewClient(api.URL + "/identity/v3/")
	if err != nil {
		t.Fatal(err)
	}
	provider.HTTPClient = *api.Client()
	provider.SetToken("synthetic-token")
	client := &Client{
		provider: provider,
		compute:  serviceClient(provider, api.URL+"/v2.1/"+projectID+"/", "compute", ""),
		cloud: config.CloudConfig{
			ProjectID: projectID, Region: "test-region",
			RequestTimeout: config.Duration{Duration: time.Second},
			PollInterval:   config.Duration{Duration: time.Millisecond},
		},
		operationTimeout: time.Second,
	}
	target := config.Resource{Type: "vm", ID: instanceID, ProjectID: projectID, Region: "test-region"}
	if _, err := client.Apply(context.Background(), target, "start", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 1 || polls.Load() < 2 {
		t.Fatalf("posts=%d polls=%d", posts.Load(), polls.Load())
	}
}
