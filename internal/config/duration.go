package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/invopop/jsonschema"
)

// Duration represents a Go duration using seconds, minutes, or hours.
type Duration struct{ time.Duration }

// UnmarshalJSON parses the duration string.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("duration: %w", ErrInvalidConfig)
	}
	d.Duration = parsed
	return nil
}

// MarshalJSON preserves the full duration precision.
func (d Duration) MarshalJSON() ([]byte, error) {
	value := d.String()
	if d.Duration != 0 && d.Duration < time.Second && d.Duration > -time.Second {
		nanoseconds := d.Nanoseconds()
		prefix := "0."
		if nanoseconds < 0 {
			prefix = "-0."
			nanoseconds = -nanoseconds
		}
		// The configuration language intentionally excludes ns/us/ms suffixes.
		value = prefix + strings.TrimRight(fmt.Sprintf("%09d", nanoseconds), "0") + "s"
	}
	return json.Marshal(value)
}

// Std returns the standard duration.
func (d Duration) Std() time.Duration { return d.Duration }

// JSONSchema restricts input to the supported duration units.
func (Duration) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Pattern: `^(?:[0-9]+(?:\.[0-9]+)?(?:s|m|h))+$`}
}
