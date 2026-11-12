package config

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDurationJSONRoundTripPreservesSchemaCompatibleUnits(t *testing.T) {
	cleanMetadata(t)
	cfg, err := loadText(t, mainConfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []time.Duration{time.Nanosecond, 123456789 * time.Nanosecond, 500 * time.Millisecond, time.Second, time.Hour + time.Nanosecond, time.Duration(1<<63 - 1)} {
		cfg.ValidationInterval = Duration{Duration: value}
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := loadText(t, string(data))
		if err != nil {
			t.Fatalf("serialized duration %v rejected by schema: %v", value, err)
		}
		if decoded.ValidationInterval.Std() != value {
			t.Fatal("duration precision lost", value, decoded.ValidationInterval.Std())
		}
	}
}
