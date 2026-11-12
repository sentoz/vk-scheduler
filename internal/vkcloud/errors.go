package vkcloud

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/gophercloud/gophercloud/v2"
)

var (
	// ErrAttemptExhausted prevents reuse of process-local attempt IDs.
	ErrAttemptExhausted = errors.New("operation attempt counter exhausted")
	// ErrUnconfirmedOperation blocks new actions until an earlier outcome is confirmed.
	ErrUnconfirmedOperation = errors.New("previous cloud operation is unconfirmed")
	// ErrMissingCredentials indicates an incomplete authentication configuration.
	ErrMissingCredentials = errors.New("missing credentials")
	// ErrInvalidCredentials indicates rejected or expired authentication.
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrPermissionDenied indicates insufficient API permissions.
	ErrPermissionDenied = errors.New("permission denied")
	// ErrResourceNotFound indicates a missing or deleted resource.
	ErrResourceNotFound = errors.New("resource not found")
	// ErrScopeMismatch indicates a resource or token outside the configured scope.
	ErrScopeMismatch = errors.New("scope mismatch")
	// ErrEndpointNotFound indicates a missing or ambiguous service endpoint.
	ErrEndpointNotFound = errors.New("service endpoint unavailable")
	// ErrUnsupportedState indicates a state requiring an unsupported action.
	ErrUnsupportedState = errors.New("unsupported resource state")
	// ErrUnexpectedState indicates an unknown resource state.
	ErrUnexpectedState = errors.New("unexpected resource state")
	// ErrResourceFailed indicates a terminal cloud failure.
	ErrResourceFailed = errors.New("resource failed")
	// ErrOperationTimeout indicates an operation that exceeded its deadline.
	ErrOperationTimeout = errors.New("operation timed out")
	// ErrOperationConflict indicates a resource that cannot accept an action.
	ErrOperationConflict = errors.New("operation conflict")
	// ErrUnexpectedResponse indicates a malformed or unexpected API response.
	ErrUnexpectedResponse = errors.New("unexpected API response")
)

type apiError struct {
	operation string
	status    int
	kind      error
	cause     error
	requestID string
	hint      string
}

func (e *apiError) Error() string {
	message := fmt.Sprintf("vkcloud %s: %v (http_status=%d request_id=%s)", e.operation, e.kind, e.status, e.requestID)
	if e.hint != "" {
		message += "; " + e.hint
	}
	return message
}

func (e *apiError) Unwrap() []error {
	return []error{e.kind, e.cause}
}

func safeAPIError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var reauth *gophercloud.ErrUnableToReauthenticate
	if errors.As(err, &reauth) {
		err = errors.Join(reauth.ErrOriginal, reauth.ErrReauth)
	}
	kind := ErrUnexpectedResponse
	status := 0
	requestID := ""
	hint := ""
	var nested *apiError
	if errors.As(err, &nested) {
		hint = nested.hint
	}
	var response gophercloud.ErrUnexpectedResponseCode
	if errors.As(err, &response) {
		status = response.Actual
		requestID = response.ResponseHeader.Get("X-Openstack-Request-Id")
		if requestID == "" {
			requestID = response.ResponseHeader.Get("X-Request-Id")
		}
		if !requestIDPattern.MatchString(requestID) {
			requestID = ""
		}
		switch status {
		case 401:
			kind = ErrInvalidCredentials
		case 403:
			kind = ErrPermissionDenied
		case 404:
			kind = ErrResourceNotFound
			if operation == "authenticate" || operation == "reauthenticate" {
				// Keystone also uses 404 for an expired or revoked source token.
				hint = "check identity URL, project and source credential; OS_TOKEN may be expired or revoked"
			}
		case 409:
			kind = ErrOperationConflict
		}
	}
	if errors.Is(err, ErrScopeMismatch) {
		kind = ErrScopeMismatch
	} else if errors.Is(err, ErrInvalidCredentials) {
		kind = ErrInvalidCredentials
	}
	if errors.Is(err, context.Canceled) {
		kind = context.Canceled
	} else if errors.Is(err, context.DeadlineExceeded) {
		kind = context.DeadlineExceeded
	}
	return &apiError{operation: operation, status: status, kind: kind, cause: err, requestID: requestID, hint: hint}
}

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
