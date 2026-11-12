package vkcloud

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/sentoz/vk-scheduler/internal/config"
)

func TestAction401RefreshIsBoundedAndValidatesCandidate(t *testing.T) {
	for _, authType := range []string{"v3password", "v3token"} {
		t.Run(authType, func(t *testing.T) { testAction401Refresh(t, authType) })
	}
}

func testAction401Refresh(t *testing.T, authType string) {
	t.Helper()
	for _, mode := range []string{"success", "second-401", "wrong-project", "expired-refresh", "rejected-source"} {
		t.Run(mode, func(t *testing.T) {
			var authCalls, postCalls, effects, permits atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.UserAgent(), "vk-scheduler/test") {
					t.Error("reauth lost application User-Agent")
				}
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/infra/identity/v3/auth/tokens" {
					var request struct {
						Auth struct {
							Identity struct {
								Methods []string `json:"methods"`
								Token   struct {
									ID string `json:"id"`
								} `json:"token"`
							} `json:"identity"`
						} `json:"auth"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					method := "password"
					if authType == "v3token" {
						method = "token"
						if request.Auth.Identity.Token.ID != "permanent-source-token" {
							t.Error("refresh used a derived access token instead of the source credential")
						}
					}
					if len(request.Auth.Identity.Methods) != 1 || request.Auth.Identity.Methods[0] != method {
						t.Error("refresh changed authentication method")
					}
					project, token := testProject, "old-token"
					expires := time.Now().Add(time.Hour)
					if authCalls.Add(1) > 1 {
						if mode == "rejected-source" {
							w.WriteHeader(http.StatusUnauthorized)
							return
						}
						token = "new-token"
						if mode == "wrong-project" {
							project = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
						}
						if mode == "expired-refresh" {
							expires = time.Now().Add(-time.Hour)
						}
					}
					w.Header().Set("X-Subject-Token", token)
					w.WriteHeader(201)
					if err := json.NewEncoder(w).Encode(map[string]any{"token": map[string]any{"expires_at": expires.UTC().Format(time.RFC3339), "project": map[string]string{"id": project}, "catalog": []any{}}}); err != nil {
						t.Error(err)
					}
					return
				}
				if r.Method == http.MethodPost {
					postCalls.Add(1)
					var body map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if len(body) != 1 || string(body["os-start"]) != "null" {
						t.Error("401 replay changed POST body")
					}
					if r.Header.Get("X-Auth-Token") != "new-token" || mode == "second-401" {
						w.WriteHeader(401)
						return
					}
					effects.Add(1)
					w.WriteHeader(202)
					return
				}
				state := "SHUTOFF"
				if effects.Load() > 0 {
					state = "ACTIVE"
				}
				if err := json.NewEncoder(w).Encode(map[string]any{"server": map[string]string{"id": testID, "tenant_id": testProject, "status": state}}); err != nil {
					t.Error(err)
				}
			}))
			defer api.Close()
			cloud := config.CloudConfig{AuthURL: api.URL + "/infra/identity/v3/", ProjectID: testProject, Region: "test-region", UserDomainName: "users", RequestTimeout: config.Duration{Duration: time.Second}, PollInterval: config.Duration{Duration: time.Millisecond}}
			provider, err := openstack.NewClient(cloud.AuthURL)
			if err != nil {
				t.Fatal(err)
			}
			provider.HTTPClient = *api.Client()
			provider.UserAgent.Prepend("vk-scheduler/test")
			auth := AuthConfig{Type: "v3password", Username: "user", Password: "synthetic-password"}
			if authType == "v3token" {
				auth = AuthConfig{Type: "v3token", Token: "permanent-source-token"}
			}
			if err := authenticateProvider(context.Background(), provider, cloud, auth); err != nil {
				t.Fatal(err)
			}
			client := &Client{provider: provider, cloud: cloud, compute: serviceClient(provider, api.URL+"/compute/", "compute", ""), operationTimeout: 2 * time.Second}
			_, err = client.Apply(context.Background(), testResource("vm"), "start", func(context.Context) error { permits.Add(1); return nil })
			if authCalls.Load() != 2 || permits.Load() != 1 || len(client.PendingOperations()) != 0 {
				t.Fatal("unexpected auth/permit/marker lifecycle", authCalls.Load(), permits.Load(), err)
			}
			switch mode {
			case "success":
				if err != nil || effects.Load() != 1 || postCalls.Load() != 2 {
					t.Fatal("authorized replay failed", err)
				}
			case "second-401":
				if !errors.Is(err, ErrInvalidCredentials) || postCalls.Load() != 2 || effects.Load() != 0 {
					t.Fatal("401 replay loop or missing classification", err)
				}
			default:
				want := ErrScopeMismatch
				if mode == "expired-refresh" || mode == "rejected-source" {
					want = ErrInvalidCredentials
				}
				if !errors.Is(err, want) || provider.Token() != "old-token" || postCalls.Load() != 1 || effects.Load() != 0 {
					t.Fatal("invalid refresh token became visible", err)
				}
			}
		})
	}
}
