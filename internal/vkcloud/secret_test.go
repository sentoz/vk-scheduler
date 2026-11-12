package vkcloud

import (
	"errors"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
)

func TestSafeAPIErrorDoesNotExposeBody(t *testing.T) {
	const canary = "synthetic-secret-do-not-publish"
	cause := gophercloud.ErrUnexpectedResponseCode{
		Actual:   403,
		Expected: []int{200},
		Body:     []byte(`{"message":"` + canary + `"}`),
	}
	err := safeAPIError("get cluster", cause)
	if strings.Contains(err.Error(), canary) {
		t.Fatal("response body leaked into public error")
	}
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatal("permission classification was lost")
	}
	var sdkError gophercloud.ErrUnexpectedResponseCode
	if !errors.As(err, &sdkError) {
		t.Fatal("SDK cause was lost")
	}
}

func TestSafeAPIErrorKeepsReauthScopeFailure(t *testing.T) {
	err := safeAPIError("get instance", &gophercloud.ErrUnableToReauthenticate{
		ErrOriginal: gophercloud.ErrUnexpectedResponseCode{Actual: 401},
		ErrReauth:   ErrScopeMismatch,
	})
	if !errors.Is(err, ErrScopeMismatch) {
		t.Fatal("reauth scope failure was lost")
	}
}

func TestStatePriorities(t *testing.T) {
	cases := []struct {
		name         string
		read         func() (string, bool, error)
		state        string
		transitional bool
		wantErr      error
	}{
		{"nova task", func() (string, bool, error) { return novaState("ACTIVE", "powering-off") },
			"powering-off", true, nil},
		{"magnum arbitrary in-progress precedes current", func() (string, bool, error) { return magnumState("ROLLBACK_IN_PROGRESS", "RUNNING") }, "ROLLBACK_IN_PROGRESS", true, nil},
		{"nova error precedes task", func() (string, bool, error) { return novaState("ERROR", "powering-on") },
			"", false, ErrResourceFailed},
		{"magnum failure precedes current", func() (string, bool, error) { return magnumState("UPDATE_FAILED", "RUNNING") },
			"", false, ErrResourceFailed},
		{"magnum generic complete", func() (string, bool, error) { return magnumState("CREATE_COMPLETE", "SHUTOFF") },
			"stopped", false, nil},
		{"magnum explicit conflict", func() (string, bool, error) { return magnumState("TURNED_ON", "SHUTOFF") },
			"transitioning", true, nil},
		{"magnum unknown current", func() (string, bool, error) { return magnumState("TURNED_ON", "FUTURE_STATE") },
			"", false, ErrUnexpectedState},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			state, transitional, err := test.read()
			if state != test.state || transitional != test.transitional || !errors.Is(err, test.wantErr) {
				t.Fatalf("state=%q transitional=%v err=%v", state, transitional, err)
			}
		})
	}
}
