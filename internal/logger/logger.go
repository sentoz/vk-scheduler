package logger

import (
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Logger holds configuration options for the application logger.
type Logger struct {
	Level  string `long:"log-level" env:"LOG_LEVEL" description:"Log level (trace, debug, info, warn, error)" default:"info"`
	Format string `long:"log-format" env:"LOG_FORMAT" description:"Log format (json or console)" default:"console"`
}

// Setup initializes the global logger based on provided configuration.
// It configures the output format (JSON or Console) and the logging level.
func (l *Logger) Setup() error {
	level, err := zerolog.ParseLevel(l.Level)
	if err != nil || level < zerolog.TraceLevel || level > zerolog.ErrorLevel {
		return ErrInvalidLevel
	}
	if l.Format != "json" && l.Format != "console" {
		return ErrInvalidFormat
	}

	zerolog.SetGlobalLevel(level)

	if l.Format == "json" {
		log.Logger = zerolog.New(os.Stderr).With().Timestamp().Logger()
		return nil
	}

	output := zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: time.RFC3339,
	}

	// If stderr is not a TTY (e.g. redirected to file), disable colors.
	if stat, err := os.Stderr.Stat(); err == nil {
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			output.NoColor = true
		}
	}

	log.Logger = log.Output(output)
	return nil
}
