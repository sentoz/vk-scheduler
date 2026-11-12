package config

import "errors"

var (
	// ErrInvalidResourceType reports that unsupported resource type.
	ErrInvalidResourceType = errors.New("unsupported resource type")
	// ErrInvalidResourceID reports an ID invalid for the selected resource type.
	ErrInvalidResourceID = errors.New("invalid resource ID")
	// ErrInvalidProjectID reports that invalid project ID.
	ErrInvalidProjectID = errors.New("invalid project ID")
	// ErrInvalidRegion reports that invalid region.
	ErrInvalidRegion = errors.New("invalid region")
	// ErrResourceScopeMismatch reports that resource is outside configured scope.
	ErrResourceScopeMismatch = errors.New("resource is outside configured scope")
	// ErrInvalidConcurrency reports that concurrency must be positive.
	ErrInvalidConcurrency = errors.New("concurrency must be positive")
	// ErrInvalidOperationTimeout reports that invalid operation timeout.
	ErrInvalidOperationTimeout = errors.New("invalid operation timeout")
	// ErrInvalidConfig reports that invalid configuration.
	ErrInvalidConfig = errors.New("invalid configuration")
	// ErrAssigningExpansion reports that assigning environment expansion is forbidden.
	ErrAssigningExpansion = errors.New("assigning environment expansion is forbidden")
)
