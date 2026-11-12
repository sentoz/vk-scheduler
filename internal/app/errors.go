package app

import "errors"

// ErrAlreadyRun prevents starting a terminated application instance again.
var ErrAlreadyRun = errors.New("application can only run once")
