package executor

import "errors"

var (
	// ErrInvalidExecutor reports that invalid executor configuration.
	ErrInvalidExecutor = errors.New("invalid executor configuration")
	// ErrClosed reports that executor admission is closed.
	ErrClosed = errors.New("executor admission is closed")
	// ErrGenerationExhausted reports that generation counter exhausted.
	ErrGenerationExhausted = errors.New("generation counter exhausted")
	// ErrInvalidCandidate reports that candidate is invalid or already consumed.
	ErrInvalidCandidate = errors.New("candidate is invalid or already consumed")
	// ErrStaleCandidate reports that candidate base generation is stale.
	ErrStaleCandidate = errors.New("candidate base generation is stale")
	// ErrUnsupportedAction reports that unsupported action.
	ErrUnsupportedAction = errors.New("unsupported action")
	// ErrInvalidPermit reports that invalid mutation permit.
	ErrInvalidPermit = errors.New("invalid mutation permit")
	// ErrResourceRemoved reports that resource is no longer managed.
	ErrResourceRemoved = errors.New("resource is no longer managed")
	// ErrSuperseded reports that intent superseded before mutation.
	ErrSuperseded = errors.New("intent superseded before mutation")
)
