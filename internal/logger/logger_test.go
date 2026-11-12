package logger

import (
	"errors"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"testing"
)

func TestSetupValidatesBeforeChangingGlobalLogger(t *testing.T) {
	previous, level := log.Logger, zerolog.GlobalLevel()
	defer func() { log.Logger = previous; zerolog.SetGlobalLevel(level) }()
	for _, item := range []struct {
		level, format string
		want          error
	}{{"unknown", "json", ErrInvalidLevel}, {"fatal", "json", ErrInvalidLevel}, {"info", "unknown", ErrInvalidFormat}, {"debug", "json", nil}, {"warn", "console", nil}} {
		l := Logger{Level: item.level, Format: item.format}
		if err := l.Setup(); !errors.Is(err, item.want) {
			t.Fatal(item, err)
		}
	}
}
