package calendar

import "errors"

var (
	// ErrInvalidRange reports invalid dates or a range longer than 366 days.
	ErrInvalidRange = errors.New("calendar requires both YYYY-MM-DD dates and at most 366 days")
	// ErrEventLimit prevents unbounded calendar responses.
	ErrEventLimit = errors.New("calendar exceeds 100000 events; select a shorter range")
	// ErrResponseLimit prevents large metadata from producing an unbounded JSON response.
	ErrResponseLimit = errors.New("calendar exceeds 64 MiB response budget; select a shorter range")
)
