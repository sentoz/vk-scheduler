package vkcloud

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"
	"github.com/sentoz/vk-scheduler/internal/config"
)

func TestTokenAuthScopeExpiryAndBoundedReauth(t *testing.T) {
	for _, item := range []struct {
		name, project string
		expires       time.Time
		want          error
	}{{"valid", testProject, time.Now().Add(time.Hour), nil}, {"scope", "other", time.Now().Add(time.Hour), ErrScopeMismatch}, {"expired", testProject, time.Now().Add(-time.Hour), ErrInvalidCredentials}} {
		t.Run(item.name, func(t *testing.T) {
			var calls atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.WriteHeader(401)
					return
				}
				calls.Add(1)
				var body struct {
					Auth struct {
						Identity struct {
							Methods []string `json:"methods"`
							Token   struct {
								ID string `json:"id"`
							} `json:"token"`
						} `json:"identity"`
						Scope struct {
							Project struct {
								ID string `json:"id"`
							} `json:"project"`
						} `json:"scope"`
					} `json:"auth"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if len(body.Auth.Identity.Methods) != 1 || body.Auth.Identity.Methods[0] != "token" || body.Auth.Identity.Token.ID != "input-token" || body.Auth.Scope.Project.ID != testProject {
					t.Error("invalid scoped token request")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Subject-Token", "scoped-token")
				w.WriteHeader(201)
				if err := json.NewEncoder(w).Encode(map[string]any{"token": map[string]any{"expires_at": item.expires.UTC().Format(time.RFC3339), "project": map[string]string{"id": item.project}, "catalog": []any{}}}); err != nil {
					t.Error(err)
				}
			}))
			defer api.Close()
			p, err := openstack.NewClient(api.URL + "/infra/identity/v3/")
			if err != nil {
				t.Fatal(err)
			}
			p.HTTPClient = *api.Client()
			err = authenticateProvider(context.Background(), p, config.CloudConfig{ProjectID: testProject, AuthURL: api.URL + "/infra/identity/v3/", RequestTimeout: config.Duration{Duration: time.Second}}, AuthConfig{Type: "v3token", Token: "input-token"})
			if !errors.Is(err, item.want) {
				t.Fatal(err)
			}
			if item.want != nil {
				return
			}
			if p.ReauthFunc == nil {
				t.Fatal("scoped token refresh is unavailable")
			}
			service := serviceClient(p, api.URL+"/", "compute", "")
			var result any
			_, err = service.Get(context.Background(), api.URL+"/servers/test", &result, nil)
			if err == nil || calls.Load() != 2 {
				t.Fatal("401 refresh was missing or unbounded", calls.Load(), err)
			}
		})
	}
}

func TestReadRetriesExcludeRedirectAndTLSPolicyErrors(t *testing.T) {
	for _, cause := range []error{ErrUnexpectedResponse, x509.UnknownAuthorityError{}} {
		err := &url.Error{Op: "Get", URL: "https://example.test/", Err: cause}
		if retryableRead(err) {
			t.Fatal("permanent transport policy error was retried")
		}
	}
}

func TestAuthEnvironmentAndEndpointValidation(t *testing.T) {
	for _, key := range []string{"OS_USERNAME", "OS_PASSWORD", "OS_TOKEN"} {
		t.Setenv(key, "")
	}
	if _, err := AuthFromEnv("v3password"); !errors.Is(err, ErrMissingCredentials) {
		t.Fatal(err)
	}
	t.Setenv("OS_USERNAME", "user")
	t.Setenv("OS_PASSWORD", "  keep spaces  ")
	auth, err := AuthFromEnv("v3password")
	if err != nil || auth.Password != "  keep spaces  " {
		t.Fatal("password trimmed")
	}
	t.Setenv("OS_TOKEN", "token")
	if _, err := AuthFromEnv("v3password"); err == nil {
		t.Fatal("ambiguous auth accepted")
	}
	for _, raw := range []string{"http://example.test/v1/", "https://user:password@example.test/v1/", "https://example.test/v1/?q=secret", "https://example.test/v1/#fragment"} {
		if _, err := validateServiceURL(raw); err == nil {
			t.Fatal("invalid endpoint accepted")
		}
	}
	base := "https://compute.example.test/v2.1/" + testProject
	normalized, err := validateServiceURL(base)
	if err != nil || normalized != base+"/" {
		t.Fatal("service path altered")
	}
	catalog := &tokens.ServiceCatalog{Entries: []tokens.CatalogEntry{{Type: "compute", Endpoints: []tokens.Endpoint{{Region: "one", Interface: "public", URL: base}}}}}
	if _, err := catalogEndpoint(catalog, "compute", "two", "public"); !errors.Is(err, ErrEndpointNotFound) {
		t.Fatal("wrong region fallback")
	}
	catalog.Entries[0].Endpoints = append(catalog.Entries[0].Endpoints, catalog.Entries[0].Endpoints[0])
	if _, err := catalogEndpoint(catalog, "compute", "one", "public"); err == nil {
		t.Fatal("ambiguous catalog accepted")
	}
}

func TestReadRetriesAreBoundedAndSafe(t *testing.T) {
	var calls, pauses int
	err := readWithRetry(context.Background(), func(context.Context) error { calls++; return gophercloud.ErrUnexpectedResponseCode{Actual: 503} }, func(context.Context, time.Duration) error { pauses++; return nil })
	if err == nil || calls != 3 || pauses != 2 {
		t.Fatal(calls, pauses, err)
	}
	if delay := readRetryDelay(gophercloud.ErrUnexpectedResponseCode{Actual: 429, ResponseHeader: http.Header{"Retry-After": []string{"999999"}}}, time.Second, time.Now()); delay != 5*time.Second {
		t.Fatal(delay)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	calls = 0
	if err := readWithRetry(cancelled, func(context.Context) error { calls++; return nil }, waitContext); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("cancelled retry ran")
	}
	safe := safeAPIError("get instance", gophercloud.ErrUnexpectedResponseCode{Actual: 403, Body: []byte("SECRET_CANARY"), ResponseHeader: http.Header{"X-Openstack-Request-Id": []string{"req-synthetic"}}})
	if strings.Contains(safe.Error(), "SECRET_CANARY") || !strings.Contains(safe.Error(), "req-synthetic") {
		t.Fatal("unsafe diagnostic", safe)
	}
}
