package vkcloud

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sentoz/vk-scheduler/internal/config"
)

func TestClientConstructorServiceSelectionAndRedirectPolicy(t *testing.T) {
	var catalog atomic.Value
	catalog.Store([]any{})
	var redirects atomic.Int32
	var authCalls atomic.Int32
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { destinationCalls.Add(1); w.WriteHeader(202) }))
	defer destination.Close()
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/infra/identity/v3/auth/tokens" {
			authCalls.Add(1)
			w.Header().Set("X-Subject-Token", "synthetic-token")
			w.WriteHeader(201)
			if err := json.NewEncoder(w).Encode(map[string]any{"token": map[string]any{"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "project": map[string]string{"id": testProject}, "catalog": catalog.Load()}}); err != nil {
				t.Error(err)
			}
			return
		}
		if redirects.Load() == 1 || (redirects.Load() == 2 && r.Method == http.MethodPost) {
			http.Redirect(w, r, destination.URL+"/", http.StatusTemporaryRedirect)
			return
		}
		if r.Method == http.MethodGet {
			if err := json.NewEncoder(w).Encode(map[string]any{"server": map[string]string{"id": testID, "tenant_id": testProject, "status": "SHUTOFF"}}); err != nil {
				t.Error(err)
			}
			return
		}
		w.WriteHeader(202)
	}))
	defer api.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", ca)
	cloud := config.CloudConfig{AuthURL: api.URL + "/infra/identity/v3/", ProjectID: testProject, Region: "test-region", UserDomainName: "users", Interface: "public", MaxConcurrentReads: 2, RequestTimeout: config.Duration{Duration: time.Second}, PollInterval: config.Duration{Duration: time.Millisecond}, MagnumMicroversion: "1.33"}
	auth := AuthConfig{Type: "v3password", Username: "user", Password: "synthetic-password"}
	entry := func(kind, region, iface string) any {
		path := "compute"
		switch kind {
		case "container-infra":
			path = "magnum"
		case "managed-k8s":
			path = "infra/container/managed-kubernetes/v1"
		}
		return map[string]any{"type": kind, "endpoints": []any{map[string]string{"region": region, "interface": iface, "url": api.URL + "/" + path + "/"}}}
	}
	compute := entry("compute", "test-region", "public")
	magnum := entry("container-infra", "test-region", "public")
	managed := entry("managed-k8s", "test-region", "public")
	for _, item := range []struct {
		name                 string
		catalog              []any
		targets              []config.Resource
		vm, cluster, managed bool
		want                 error
	}{
		{"VM only", []any{compute}, []config.Resource{testResource("vm")}, true, false, false, nil},
		{"Magnum only", []any{magnum}, []config.Resource{testResource("k8s_v1")}, false, true, false, nil},
		{"Managed only", []any{managed}, []config.Resource{testResource("k8s_v2")}, false, false, true, nil},
		{"Managed missing", []any{magnum}, []config.Resource{testResource("k8s_v2")}, false, false, false, ErrEndpointNotFound},
		{"Managed wrong region", []any{entry("managed-k8s", "other", "public")}, []config.Resource{testResource("k8s_v2")}, false, false, false, ErrEndpointNotFound},
		{"Managed wrong interface", []any{entry("managed-k8s", "test-region", "internal")}, []config.Resource{testResource("k8s_v2")}, false, false, false, ErrEndpointNotFound},
		{"Managed ambiguous", []any{managed, managed}, []config.Resource{testResource("k8s_v2")}, false, false, false, ErrEndpointNotFound},
		{"empty bundle", []any{}, nil, false, false, false, nil},
		{"missing catalog", []any{}, []config.Resource{testResource("vm")}, false, false, false, ErrEndpointNotFound},
		{"wrong region", []any{entry("compute", "other", "public")}, []config.Resource{testResource("vm")}, false, false, false, ErrEndpointNotFound},
		{"wrong interface", []any{entry("compute", "test-region", "internal")}, []config.Resource{testResource("vm")}, false, false, false, ErrEndpointNotFound},
		{"ambiguous", []any{compute, compute}, []config.Resource{testResource("vm")}, false, false, false, ErrEndpointNotFound},
	} {
		t.Run(item.name, func(t *testing.T) {
			catalog.Store(item.catalog)
			client, err := NewClient(context.Background(), cloud, auth, item.targets, time.Minute)
			if !errors.Is(err, item.want) {
				t.Fatal(err)
			}
			if item.want != nil {
				return
			}
			if (client.service("vm") != nil) != item.vm || (client.service("k8s_v1") != nil) != item.cluster || (client.service("k8s_v2") != nil) != item.managed {
				t.Fatal("unexpected service dependency")
			}
			if err := client.ValidateCredentials(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := client.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := client.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
	catalog.Store([]any{})
	override := cloud
	override.ComputeEndpoint = api.URL + "/v2.1/" + testProject
	client, err := NewClient(context.Background(), override, auth, []config.Resource{testResource("vm")}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint := client.service("vm").Endpoint; endpoint != override.ComputeEndpoint+"/" {
		t.Fatalf("version/project duplicated: %s", endpoint)
	}
	if err := client.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	override.ManagedK8sEndpoint = api.URL + "/infra/container/managed-kubernetes/v1"
	client, err = NewClient(t.Context(), override, auth, []config.Resource{testResource("k8s_v2")}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint := client.service("k8s_v2").Endpoint; endpoint != override.ManagedK8sEndpoint+"/" {
		t.Fatal("Managed endpoint version changed", endpoint)
	}
	if err := client.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	catalog.Store([]any{compute, magnum, managed})
	client, err = NewClient(context.Background(), cloud, auth, []config.Resource{testResource("vm")}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := client.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	oldCompute := client.service("vm")
	var concurrent sync.WaitGroup
	for range 8 {
		concurrent.Go(func() {
			if err := client.PrepareServices([]config.Resource{testResource("k8s_v1"), testResource("k8s_v2")}); err != nil {
				t.Error(err)
			}
		})
	}
	concurrent.Wait()
	if client.service("vm") != oldCompute || client.service("k8s_v1") == nil || client.service("k8s_v2") == nil {
		t.Fatal("service preparation replaced working clients")
	}
	for _, mode := range []int32{1, 2} {
		redirects.Store(mode)
		if mode == 1 {
			_, _, err = client.GetState(context.Background(), testResource("vm"))
		} else {
			_, err = client.Apply(context.Background(), testResource("vm"), "start", func(context.Context) error { return nil })
		}
		if err == nil || destinationCalls.Load() != 0 {
			t.Fatal("cloud redirect followed", err)
		}
	}

	before := authCalls.Load()
	invalid := cloud
	invalid.RequestTimeout = config.Duration{}
	if _, err := NewClient(context.Background(), invalid, auth, nil, time.Minute); err == nil {
		t.Fatal("invalid timeout accepted")
	}
	if _, err := NewClient(context.Background(), cloud, AuthConfig{Type: "unknown"}, nil, time.Minute); err == nil {
		t.Fatal("invalid auth mode accepted")
	}
	badTarget := testResource("vm")
	badTarget.ID = "invalid"
	if _, err := NewClient(context.Background(), cloud, auth, []config.Resource{badTarget}, time.Minute); err == nil {
		t.Fatal("invalid target accepted")
	}
	if authCalls.Load() != before {
		t.Fatal("invalid client configuration reached the network")
	}
	invalid = cloud
	invalid.ManagedK8sEndpoint = "http://invalid.example/v1"
	if _, err := NewClient(t.Context(), invalid, auth, []config.Resource{testResource("k8s_v2")}, time.Minute); err == nil || authCalls.Load() != before {
		t.Fatal("invalid Managed endpoint reached network", err)
	}

	catalog.Store([]any{compute})
	cold, err := NewClient(context.Background(), cloud, auth, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := cold.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	if err := cold.PrepareServices([]config.Resource{testResource("vm"), testResource("k8s_v1")}); !errors.Is(err, ErrEndpointNotFound) {
		t.Fatal("missing service accepted", err)
	}
	if cold.service("vm") != nil || cold.service("k8s_v1") != nil {
		t.Fatal("failed preparation published a partial service set")
	}
	if err := cold.PrepareServices([]config.Resource{testResource("vm"), testResource("k8s_v2")}); !errors.Is(err, ErrEndpointNotFound) {
		t.Fatal("missing Managed service accepted", err)
	}
	if cold.service("vm") != nil || cold.service("k8s_v2") != nil {
		t.Fatal("failed Managed preparation published a partial service set")
	}
}
