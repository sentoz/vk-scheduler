package integration_test

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sentoz/vk-scheduler/internal/app"
	"github.com/sentoz/vk-scheduler/internal/config"
	"github.com/sentoz/vk-scheduler/internal/vkcloud"
)

func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for lifecycle event")
		var zero T
		return zero
	}
}

func TestAppWithRealKeystoneNovaMagnumAndManagedK8sAdapter(t *testing.T) {
	const project = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const vm = "11111111-1111-4111-8111-111111111111"
	const cluster = "22222222-2222-4222-8222-222222222222"
	const managed = "3AbCdE4FgHiJ5KlMnOpQrStUvWx"
	preflight := make(chan struct{}, 1)
	release := make(chan struct{})
	stable := make(chan string, 8)
	var released atomic.Bool
	var forbidden atomic.Bool
	var vmOn, clusterOn, managedOn atomic.Bool
	var vmReads, clusterReads, managedReads, posts atomic.Int32
	var api *httptest.Server
	api = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		write := func(value any) {
			if err := json.NewEncoder(w).Encode(value); err != nil {
				t.Error(err)
			}
		}
		if r.URL.Path == "/infra/identity/v3/auth/tokens" {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			w.Header().Set("X-Subject-Token", "synthetic-token")
			w.WriteHeader(201)
			write(map[string]any{"token": map[string]any{"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "project": map[string]string{"id": project}, "catalog": []any{map[string]any{"type": "compute", "endpoints": []any{map[string]string{"region": "test-region", "interface": "public", "url": api.URL + "/compute/"}}}, map[string]any{"type": "container-infra", "endpoints": []any{map[string]string{"region": "test-region", "interface": "public", "url": api.URL + "/magnum/"}}}, map[string]any{"type": "managed-k8s", "endpoints": []any{map[string]string{"region": "test-region", "interface": "public", "url": api.URL + "/infra/container/managed-kubernetes/v1"}}}}}})
			return
		}
		if r.Header.Get("X-Auth-Token") != "synthetic-token" {
			t.Error("missing scoped token")
			w.WriteHeader(401)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/magnum/") && r.Header.Get("MCS-API-Version") != "container-infra 1.33" {
			t.Error("missing VK version header")
		}
		managedURL := "/infra/container/managed-kubernetes/v1/clusters/" + managed
		if strings.HasPrefix(r.URL.Path, managedURL) && r.Header.Get("MCS-API-Version") != "" {
			t.Error("Managed-k8s received Magnum header")
		}
		if forbidden.Load() {
			w.WriteHeader(403)
			return
		}
		if r.Method == http.MethodPost {
			posts.Add(1)
			if r.URL.Path == "/compute/servers/"+vm+"/action" {
				vmOn.Store(true)
				w.WriteHeader(202)
				return
			}
			if r.URL.Path == "/magnum/clusters/"+cluster+"/actions/" {
				clusterOn.Store(true)
				w.WriteHeader(202)
				write(map[string]string{"uuid": cluster})
				return
			}
			if r.URL.Path == managedURL+"/turn_on" {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body == nil || len(body) != 0 {
					t.Error("invalid Managed-k8s command", err)
				}
				managedOn.Store(true)
				write(map[string]any{})
				return
			}
			t.Errorf("unexpected POST %s", r.URL.Path)
			w.WriteHeader(400)
			return
		}
		if !released.Load() {
			select {
			case preflight <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if r.URL.Path == "/compute/servers/"+vm {
			state, task := "SHUTOFF", ""
			if vmOn.Load() {
				state = "ACTIVE"
				if vmReads.Add(1) < 3 {
					task = "powering-on"
				} else {
					select {
					case stable <- "vm":
					default:
					}
				}
			}
			write(map[string]any{"server": map[string]any{"id": vm, "tenant_id": project, "status": state, "OS-EXT-STS:task_state": task}})
			return
		}
		if r.URL.Path == "/magnum/clusters/"+cluster {
			state, legacy := "SHUTOFF", "TURNED_OFF"
			if clusterOn.Load() {
				state = "RUNNING"
				legacy = "TURNED_ON"
				if clusterReads.Add(1) < 3 {
					legacy = "TURNING_ON"
				} else {
					select {
					case stable <- "cluster":
					default:
					}
				}
			}
			write(map[string]string{"uuid": cluster, "project_id": project, "new_status": state, "status": legacy})
			return
		}
		if r.URL.Path == managedURL {
			state := "SHUTOFF"
			if managedOn.Load() {
				state = "TURNING_ON"
				if managedReads.Add(1) >= 3 {
					state = "RUNNING"
					select {
					case stable <- "managed":
					default:
					}
				}
			}
			write(map[string]string{"id": managed, "project_id": project, "status": state})
			return
		}
		w.WriteHeader(404)
	}))
	defer api.Close()
	directory := t.TempDir()
	ca := filepath.Join(directory, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", ca)
	for _, key := range []string{"OS_AUTH_URL", "OS_PROJECT_ID", "OS_REGION_NAME", "OS_USER_DOMAIN_NAME", "OS_INTERFACE"} {
		t.Setenv(key, "")
	}
	schedules := filepath.Join(directory, "schedules")
	if err := os.Mkdir(schedules, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := func(name, kind, id string) string {
		return fmt.Sprintf("apiVersion: scheduler.vk/v1alpha1\nkind: Schedule\nmetadata:\n  name: %s\nspec:\n  type: cron\n  resource:\n    type: %s\n    id: %s\n  actions:\n    start:\n      enabled: true\n      crontab: '* * * * *'\n", name, kind, id)
	}
	path := filepath.Join(schedules, "all.yaml")
	publish := func(text string) {
		t.Helper()
		temp := filepath.Join(directory, "candidate.yaml")
		if err := os.WriteFile(temp, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(temp, path); err != nil {
			t.Fatal(err)
		}
	}
	text := manifest("vm", "vm", vm) + "---\n" + manifest("cluster", "k8s_v1", cluster) + "---\n" + manifest("managed", "k8s_v2", managed)
	publish(text)
	configPath := filepath.Join(directory, "config.yaml")
	configText := fmt.Sprintf("timezone: UTC\nschedules_dir: %s\nui_enabled: true\nmetrics_enabled: true\nshutdown_timeout: 2s\noperation_timeout: 5s\ncloud:\n  project_id: %s\n  region: test-region\n  auth_url: %s/infra/identity/v3/\n  request_timeout: 1s\n  poll_interval: 0.001s\n", schedules, project, api.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	client, err := vkcloud.NewClient(context.Background(), cfg.Cloud, vkcloud.AuthConfig{Type: "v3password", Username: "test-user", Password: "synthetic-password"}, []config.Resource{{Type: "vm", ID: vm, ProjectID: project, Region: "test-region"}, {Type: "k8s_v1", ID: cluster, ProjectID: project, Region: "test-region"}, {Type: "k8s_v2", ID: managed, ProjectID: project, Region: "test-region"}}, cfg.OperationTimeout.Std())
	if err != nil {
		t.Fatal(err)
	}
	application, err := app.New(*cfg, client, app.Options{Address: "127.0.0.1:0", ReloadInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	await(t, preflight)
	get := func(path string) (int, []byte) {
		t.Helper()
		response, err := http.Get("http://" + application.Address() + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := response.Body.Close(); err != nil {
				t.Error(err)
			}
		}()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, data
	}
	if code, _ := get("/health/ready"); code != 503 {
		t.Fatalf("premature readiness %d", code)
	}
	released.Store(true)
	close(release)
	seen := map[string]bool{}
	for len(seen) < 3 {
		seen[await(t, stable)] = true
	}
	if code, data := get("/api/calendar?from=" + time.Now().UTC().Format(time.DateOnly) + "&to=" + time.Now().UTC().Format(time.DateOnly)); code != 200 || !strings.Contains(string(data), `"state":"running"`) || !strings.Contains(string(data), `"project_id":"`+project+`"`) || !strings.Contains(string(data), `"resource_type":"k8s_v2"`) {
		t.Fatalf("calendar %d %.200s", code, data)
	}
	if code, data := get("/metrics"); code != 200 || !strings.Contains(string(data), "vk_scheduler_operations_total") {
		t.Fatal("metrics unavailable")
	}
	for path, content := range map[string]string{"/": `src="/app.js"`, "/app.js": "/api/calendar", "/style.css": ".app", "/info": `"build_time":`} {
		if code, data := get(path); code != 200 || !strings.Contains(string(data), content) {
			t.Fatalf("HTTP route %s: status %d", path, code)
		}
	}
	if posts.Load() != 3 {
		t.Fatalf("UI mutated resources or startup repeated: %d", posts.Load())
	}
	before := application.Snapshot().Generation
	publish(strings.Replace(text, "name: vm", "name: renamed-vm", 1))
	if err := application.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	good := application.Snapshot().Generation
	if good <= before {
		t.Fatal("generation did not advance")
	}
	forbidden.Store(true)
	publish(text)
	if err := application.Reload(ctx); err == nil {
		t.Fatal("forbidden preflight accepted")
	}
	if application.Snapshot().Generation != good {
		t.Fatal("failed preflight changed generation")
	}
	forbidden.Store(false)
	publish(text + "broken: [")
	if err := application.Reload(ctx); err == nil {
		t.Fatal("invalid reload accepted")
	}
	if application.Snapshot().Generation != good {
		t.Fatal("invalid reload changed snapshot")
	}
	publish(text)
	if err := application.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if application.Snapshot().Generation <= good+1 {
		t.Fatal("failed candidate ID reused")
	}
	publish("")
	if err := application.Reload(ctx); err != nil || len(application.Snapshot().Groups) != 0 {
		t.Fatal("empty bundle did not remove management", err)
	}
	cancel()
	if err := await(t, done); err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 3 {
		t.Fatalf("reload or shutdown mutated resources: %d", posts.Load())
	}
}
