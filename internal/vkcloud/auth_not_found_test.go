package vkcloud

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/sentoz/vk-scheduler/internal/config"
)

func TestAuthenticationNotFoundPreservesIdentityPathAndHidesCredentials(t *testing.T) {
	const canary = "synthetic-rejected-source-token"
	for _, authType := range []string{"v3token", "v3password"} {
		for _, suffix := range []string{"/infra/identity/v3", "/infra/identity/v3/"} {
			t.Run(authType+suffix, func(t *testing.T) {
				var calls atomic.Int32
				api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.Method != http.MethodPost || r.URL.Path != "/infra/identity/v3/auth/tokens" {
						t.Errorf("unexpected authentication route: %s %s", r.Method, r.URL.Path)
					}
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("X-Openstack-Request-Id", "req-auth-not-found")
					w.WriteHeader(http.StatusNotFound)
					if _, err := w.Write([]byte(`{"error":{"code":404,"message":"Could not find token: ` + canary + `."}}`)); err != nil {
						t.Error(err)
					}
				}))
				defer api.Close()
				cloud := config.CloudConfig{AuthURL: api.URL + suffix, ProjectID: testProject, UserDomainName: "Default"}
				provider, err := openstack.NewClient(cloud.AuthURL)
				if err != nil {
					t.Fatal(err)
				}
				provider.HTTPClient = *api.Client()
				auth := AuthConfig{Type: authType, Token: canary, Username: "synthetic-user", Password: canary}
				err = authenticateProvider(context.Background(), provider, cloud, auth)
				assertAuthenticationNotFound(t, err, canary)
				if !strings.Contains(err.Error(), "req-auth-not-found") || !strings.Contains(err.Error(), "http_status=404") {
					t.Fatal("authentication status or request ID was lost")
				}
				if calls.Load() != 1 || provider.ReauthFunc != nil || provider.Token() != "" {
					t.Fatal("failed authentication retried or published credentials")
				}
			})
		}
	}
}

func TestAuthenticationNotFoundHintSurvivesReauthFailure(t *testing.T) {
	const canary = "synthetic-rejected-source-token"
	refresh := safeAPIError("reauthenticate", gophercloud.ErrUnexpectedResponseCode{
		Actual: http.StatusNotFound,
		Body:   []byte(canary),
	})
	err := safeAPIError("get instance", &gophercloud.ErrUnableToReauthenticate{
		ErrOriginal: gophercloud.ErrUnexpectedResponseCode{Actual: http.StatusUnauthorized},
		ErrReauth:   refresh,
	})
	assertAuthenticationNotFound(t, err, canary)
	if !errors.Is(err, ErrInvalidCredentials) || !strings.Contains(err.Error(), "http_status=401") {
		t.Fatal("original authentication failure was lost")
	}
}

func TestResourceNotFoundHasNoAuthenticationHint(t *testing.T) {
	err := safeAPIError("get instance", gophercloud.ErrUnexpectedResponseCode{Actual: http.StatusNotFound})
	if !errors.Is(err, ErrResourceNotFound) || strings.Contains(err.Error(), "OS_TOKEN") {
		t.Fatal("resource lookup was misdiagnosed as an authentication failure")
	}
}

func assertAuthenticationNotFound(t *testing.T, err error, canary string) {
	t.Helper()
	if err == nil || !errors.Is(err, ErrResourceNotFound) {
		t.Fatal("not-found classification was lost")
	}
	if strings.Contains(err.Error(), canary) || !strings.Contains(err.Error(), "OS_TOKEN may be expired or revoked") {
		t.Fatal("authentication error leaked credentials or omitted the safe hint")
	}
	var cause gophercloud.ErrUnexpectedResponseCode
	if !errors.As(err, &cause) {
		t.Fatal("SDK cause was lost")
	}
}
