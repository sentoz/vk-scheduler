package vkcloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/sentoz/vk-scheduler/internal/config"
)

func TestPasswordReauthIsShared(t *testing.T) {
	testSharedReauth(t, "v3password")
}

func TestTokenReauthIsShared(t *testing.T) {
	testSharedReauth(t, "v3token")
}

func testSharedReauth(t *testing.T, authType string) {
	t.Helper()
	const projectID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const instanceID = "11111111-1111-4111-8111-111111111111"
	var authCalls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/infra/identity/v3/auth/tokens":
			var input struct {
				Auth struct {
					Scope struct {
						Project struct {
							ID string `json:"id"`
						} `json:"project"`
					} `json:"scope"`
					Identity struct {
						Methods []string `json:"methods"`
						Token   struct {
							ID string `json:"id"`
						} `json:"token"`
						Password struct {
							User struct {
								Name     string `json:"name"`
								Password string `json:"password"`
							} `json:"user"`
						} `json:"password"`
					} `json:"identity"`
				} `json:"auth"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error("invalid auth JSON")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			validIdentity := false
			if len(input.Auth.Identity.Methods) == 1 {
				if authType == "v3token" {
					validIdentity = input.Auth.Identity.Methods[0] == "token" && input.Auth.Identity.Token.ID == "permanent-source-token"
				} else {
					validIdentity = input.Auth.Identity.Methods[0] == "password" && input.Auth.Identity.Password.User.Name == "test-user" && input.Auth.Identity.Password.User.Password == "test-password"
				}
			}
			if input.Auth.Scope.Project.ID != projectID || !validIdentity {
				t.Error("wrong authentication options")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			token := "old-test-token"
			if authCalls.Add(1) > 1 {
				token = "new-test-token"
			}
			w.Header().Set("X-Subject-Token", token)
			w.WriteHeader(http.StatusCreated)
			if err := json.NewEncoder(w).Encode(map[string]any{
				"token": map[string]any{
					"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
					"project":    map[string]any{"id": projectID},
					"catalog":    []any{},
				},
			}); err != nil {
				t.Errorf("encode token response: %v", err)
			}
		case r.Method == http.MethodGet && r.URL.Path == "/compute/servers/"+instanceID:
			if r.Header.Get("X-Auth-Token") != "new-test-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if err := json.NewEncoder(w).Encode(map[string]any{
				"server": map[string]any{
					"id": instanceID, "tenant_id": projectID, "status": "ACTIVE",
				},
			}); err != nil {
				t.Errorf("encode instance response: %v", err)
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	cloud := config.CloudConfig{
		AuthURL: api.URL + "/infra/identity/v3/", ProjectID: projectID,
		Region: "test-region", UserDomainName: "users",
		RequestTimeout: config.Duration{Duration: time.Second},
	}
	provider, err := openstack.NewClient(cloud.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	provider.HTTPClient = *api.Client()
	auth := AuthConfig{Type: "v3password", Username: "test-user", Password: "test-password"}
	if authType == "v3token" {
		auth = AuthConfig{Type: "v3token", Token: "permanent-source-token"}
	}
	if err := authenticateProvider(context.Background(), provider, cloud, auth); err != nil {
		t.Fatal(err)
	}
	client := &Client{
		provider: provider, cloud: cloud,
		compute: serviceClient(provider, api.URL+"/compute/", "compute", ""),
	}
	target := config.Resource{Type: "vm", ID: instanceID, ProjectID: projectID, Region: "test-region"}
	var workers sync.WaitGroup
	failures := make(chan error, 12)
	for range 12 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, _, err := client.GetState(context.Background(), target)
			if err != nil {
				failures <- err
			}
		}()
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if authCalls.Load() != 2 {
		t.Fatalf("auth calls=%d, want initial auth plus one refresh", authCalls.Load())
	}
}
