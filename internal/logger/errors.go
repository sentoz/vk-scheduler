package logger

import "errors"

var (
	// ErrInvalidLevel rejects unsupported levels before logging is configured.
	ErrInvalidLevel = errors.New("LOG_LEVEL must be trace, debug, info, warn, or error")
	// ErrInvalidFormat rejects unsupported logging encodings.
	ErrInvalidFormat = errors.New("LOG_FORMAT must be json or console")
)
