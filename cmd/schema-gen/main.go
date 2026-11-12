package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/invopop/jsonschema"
	"github.com/sentoz/vk-scheduler/internal/config"
)

func main() {
	out := flag.String("out", "static/schemas/config.json", "config schema destination")
	schedules := flag.String("schedule-out", "static/schemas/schedule.json", "schedule schema destination")
	flag.Parse()
	r := jsonschema.Reflector{AllowAdditionalProperties: false}
	for _, item := range []struct {
		path, title string
		value       any
	}{{*out, "VK Scheduler Configuration", config.Config{}}, {*schedules, "VK Scheduler Schedule Manifest", config.ScheduleManifest{}}} {
		schema := r.Reflect(item.value)
		schema.Version = "http://json-schema.org/draft-07/schema#"
		schema.Title = item.title
		data, err := json.MarshalIndent(schema, "", "  ")
		if err == nil {
			err = os.MkdirAll(filepath.Dir(item.path), 0750)
		}
		if err == nil {
			err = os.WriteFile(item.path, append(data, '\n'), 0644)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
