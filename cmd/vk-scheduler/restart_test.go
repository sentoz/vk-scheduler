package main

import (
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCLIProcessRestartLosesUnconfirmedMarker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX subprocess lifecycle test")
	}
	const project = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const id = "11111111-1111-4111-8111-111111111111"
	var posts atomic.Int32
	var running atomic.Bool
	secondPost := make(chan struct{}, 1)
	var api *httptest.Server
	api = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/infra/identity/v3/auth/tokens" {
			w.Header().Set("X-Subject-Token", "synthetic-token")
			w.WriteHeader(201)
			if err := json.NewEncoder(w).Encode(map[string]any{"token": map[string]any{"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "project": map[string]string{"id": project}, "catalog": []any{map[string]any{"type": "compute", "endpoints": []any{map[string]string{"region": "test-region", "interface": "public", "url": api.URL + "/compute/"}}}}}}); err != nil {
				t.Error(err)
			}
			return
		}
		if r.URL.Path == "/compute/servers/"+id+"/action" && r.Method == http.MethodPost {
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if string(body["os-start"]) != "null" {
				t.Error("unexpected action")
			}
			if posts.Add(1) == 1 {
				w.WriteHeader(503)
				return
			}
			running.Store(true)
			w.WriteHeader(202)
			select {
			case secondPost <- struct{}{}:
			default:
			}
			return
		}
		if r.URL.Path == "/compute/servers/"+id && r.Method == http.MethodGet {
			state := "SHUTOFF"
			if running.Load() {
				state = "ACTIVE"
			}
			if err := json.NewEncoder(w).Encode(map[string]any{"server": map[string]string{"id": id, "tenant_id": project, "status": state}}); err != nil {
				t.Error(err)
			}
			return
		}
		t.Error("unexpected cloud API request")
		w.WriteHeader(404)
	}))
	defer api.Close()
	directory := t.TempDir()
	ca := filepath.Join(directory, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	schedules := filepath.Join(directory, "schedules")
	if err := os.Mkdir(schedules, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf("apiVersion: scheduler.vk/v1alpha1\nkind: Schedule\nmetadata:\n  name: start\nspec:\n  type: cron\n  resource:\n    type: vm\n    id: %s\n  actions:\n    start:\n      enabled: true\n      crontab: '* * * * *'\n", id)
	if err := os.WriteFile(filepath.Join(schedules, "start.yaml"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reservation.Addr().(*net.TCPAddr).Port
	configPath := filepath.Join(directory, "config.yaml")
	configText := fmt.Sprintf("timezone: UTC\nschedules_dir: ./schedules\nmetrics_port: %d\nmetrics_enabled: true\nvalidation_interval: 0.01s\nshutdown_timeout: 2s\noperation_timeout: 5s\ncloud:\n  project_id: %s\n  region: test-region\n  auth_url: %s/infra/identity/v3/\n  request_timeout: 1s\n  poll_interval: 0.01s\n", port, project, api.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	first := startCLIProcess(t, configPath, api.URL+"/infra/identity/v3/", ca)
	first.waitReady(t, port)
	client := &http.Client{Timeout: time.Second}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	failed := false
	for !failed {
		select {
		case <-deadline.C:
			t.Fatal("first process did not report uncertain action")
		case <-ticker.C:
			response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", port))
			if err != nil {
				t.Fatal(err)
			}
			data, readErr := io.ReadAll(response.Body)
			closeErr := response.Body.Close()
			if readErr != nil || closeErr != nil {
				t.Fatal(readErr, closeErr)
			}
			failed = strings.Contains(string(data), `status="error"`)
		}
	}
	if posts.Load() != 1 {
		t.Fatal("first process replayed an uncertain POST")
	}
	first.stop(t)
	second := startCLIProcess(t, configPath, api.URL+"/infra/identity/v3/", ca)
	second.waitReady(t, port)
	select {
	case <-secondPost:
	case <-time.After(5 * time.Second):
		t.Fatal("new process unexpectedly retained old marker")
	}
	second.stop(t)
	if posts.Load() != 2 || !running.Load() {
		t.Fatal("stateless restart contract changed")
	}
	files, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatal("runtime created state files", files)
	}
}
