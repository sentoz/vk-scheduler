package resource

import "errors"

var (
	// ErrUnsupportedResourceType reports that unsupported resource type.
	ErrUnsupportedResourceType = errors.New("unsupported resource type")
)
