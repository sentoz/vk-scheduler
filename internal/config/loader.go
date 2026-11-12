package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/woozymasta/jamle"
	"gopkg.in/yaml.v3"

	"github.com/sentoz/vk-scheduler/internal/schedule"
	"github.com/sentoz/vk-scheduler/static"
)

// Load reads and validates the restart-scoped configuration.
func Load(path string) (*Config, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	f, err := os.Open(absolute)
	if err != nil {
		return nil, fmt.Errorf("open config: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	closeErr := f.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read config: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close config: %w", closeErr)
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("config exceeds 1 MiB: %w", ErrInvalidConfig)
	}
	docs, err := decodeDocuments(data)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if len(docs) != 1 {
		return nil, fmt.Errorf("config document: %w", ErrInvalidConfig)
	}
	doc := docs[0].value
	defaults(doc)
	var result Config
	if err := decodeValidated(doc, "config.json", &result); err != nil {
		return nil, err
	}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(result.SchedulesDir) {
		result.SchedulesDir = filepath.Join(filepath.Dir(absolute), result.SchedulesDir)
	}
	return &result, nil
}

func defaults(doc map[string]any) {
	for k, v := range map[string]any{"max_concurrent_jobs": 5, "validation_resources": true, "validation_interval": "10m", "shutdown_timeout": "5m", "operation_timeout": "30m", "metrics_port": 9090, "metrics_enabled": false, "ui_enabled": false} {
		setDefault(doc, k, v)
	}
	setDefault(doc, "cloud", map[string]any{})
	cloud, ok := doc["cloud"].(map[string]any)
	if !ok {
		return
	}
	for key, env := range map[string]string{"auth_url": "OS_AUTH_URL", "project_id": "OS_PROJECT_ID", "region": "OS_REGION_NAME", "user_domain_name": "OS_USER_DOMAIN_NAME", "interface": "OS_INTERFACE"} {
		if value := os.Getenv(env); value != "" {
			cloud[key] = value
		}
	}
	for k, v := range map[string]any{"auth_url": "https://msk.cloud.vk.ru/infra/identity/v3/", "user_domain_name": "users", "interface": "public", "request_timeout": "30s", "poll_interval": "5s", "max_concurrent_reads": 8, "magnum_microversion": "1.33"} {
		setDefault(cloud, k, v)
	}
}

func checkNode(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode || n.Tag == "!!merge" {
		return ErrInvalidConfig
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || seen[key.Value] {
				return ErrInvalidConfig
			}
			seen[key.Value] = true
		}
	}
	for _, child := range n.Content {
		if err := checkNode(child); err != nil {
			return err
		}
	}
	return nil
}

type decodedDocument struct {
	number int
	value  map[string]any
}

func decodeDocuments(data []byte) ([]decodedDocument, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var result []decodedDocument
	for document := 1; ; document++ {
		var node yaml.Node
		if err := decoder.Decode(&node); err == io.EOF {
			return result, nil
		} else if err != nil {
			return nil, fmt.Errorf("document %d syntax: %w", document, ErrInvalidConfig)
		}
		if len(node.Content) == 0 || node.Content[0].Tag == "!!null" {
			continue
		}
		if err := checkNode(&node); err != nil {
			return nil, fmt.Errorf("document %d structure: %w", document, err)
		}
		if err := checkExpansionSafety(&node, os.LookupEnv); err != nil {
			return nil, fmt.Errorf("document %d expansion: %w", document, err)
		}
		raw, err := yaml.Marshal(&node)
		if err != nil {
			return nil, ErrInvalidConfig
		}
		var doc map[string]any
		if err := jamle.Unmarshal(raw, &doc); err != nil || doc == nil {
			return nil, fmt.Errorf("document %d decode: %w", document, ErrInvalidConfig)
		}
		result = append(result, decodedDocument{number: document, value: doc})
	}
}

func decodeValidated(doc map[string]any, name string, target any) error {
	data, err := static.Schemas.ReadFile("schemas/" + name)
	if err != nil {
		return fmt.Errorf("embedded schema: %w", err)
	}
	var schema any
	if err := json.Unmarshal(data, &schema); err != nil {
		return fmt.Errorf("embedded schema JSON: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(name, schema); err != nil {
		return fmt.Errorf("embedded schema resource: %w", err)
	}
	compiled, err := c.Compile(name)
	if err != nil {
		return fmt.Errorf("embedded schema compilation: %w", err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return ErrInvalidConfig
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return ErrInvalidConfig
	}
	if err := compiled.Validate(generic); err != nil {
		// Schema errors can contain scalar values, including expanded credentials.
		path := "/"
		var validation *jsonschema.ValidationError
		if errors.As(err, &validation) {
			for len(validation.Causes) > 0 {
				validation = validation.Causes[0]
			}
			for _, segment := range validation.InstanceLocation {
				if !schemaFieldNames[segment] {
					break
				}
				path += segment + "/"
			}
		}
		return fmt.Errorf("%s field %s schema validation: %w", name, path, ErrInvalidConfig)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return fmt.Errorf("%s typed decode: %w", name, ErrInvalidConfig)
	}
	return nil
}

var schemaFieldNames = func() map[string]bool {
	fields := strings.Fields("timezone schedules_dir validation_resources validation_interval shutdown_timeout operation_timeout max_concurrent_jobs metrics_enabled ui_enabled metrics_port cloud auth_url project_id region user_domain_name interface request_timeout poll_interval magnum_microversion max_concurrent_reads compute_endpoint magnum_endpoint managed_k8s_endpoint apiVersion kind metadata name annotations spec type resource id actions start stop enabled time crontab day suspend")
	result := make(map[string]bool, len(fields))
	for _, field := range fields {
		result[field] = true
	}
	return result
}()

// ParseBundle validates all captured bytes as one indivisible schedule set.
func ParseBundle(bundle CapturedBundle, cloud CloudConfig) ([]Schedule, error) {
	var schedules []Schedule
	names := map[string]bool{}
	for _, file := range bundle.Files {
		docs, err := decodeDocuments(file.Data)
		if err != nil {
			return nil, fmt.Errorf("manifest %s: %w", file.Name, err)
		}
		for _, doc := range docs {
			var manifest ScheduleManifest
			if err := decodeValidated(doc.value, "schedule.json", &manifest); err != nil {
				return nil, fmt.Errorf("manifest %s document %d: %w", file.Name, doc.number, err)
			}
			if names[manifest.Metadata.Name] {
				return nil, fmt.Errorf("manifest %s document %d duplicate name: %w", file.Name, doc.number, ErrInvalidConfig)
			}
			names[manifest.Metadata.Name] = true
			if err := validateManifest(manifest, cloud); err != nil {
				return nil, fmt.Errorf("manifest %s document %d: %w", file.Name, doc.number, err)
			}
			display := manifest.Metadata.Annotations["vk-scheduler/display-name"]
			if display == "" {
				display = manifest.Metadata.Name
			}
			schedules = append(schedules, Schedule{Name: manifest.Metadata.Name, DisplayName: display, Type: manifest.Spec.Type, Resource: manifest.Spec.Resource.withScope(cloud), Actions: manifest.Spec.Actions, Suspend: manifest.Spec.Suspend, sourceFile: file.Name, sourceDocument: doc.number})
		}
	}
	return schedules, nil
}

func validateManifest(m ScheduleManifest, cloud CloudConfig) error {
	if m.APIVersion != "scheduler.vk/v1alpha1" || m.Kind != "Schedule" || strings.TrimSpace(m.Metadata.Name) == "" {
		return ErrInvalidConfig
	}
	if err := m.Spec.Resource.withScope(cloud).Validate(cloud.ProjectID, cloud.Region); err != nil {
		return err
	}
	enabled := false
	for _, a := range []*ActionConfig{m.Spec.Actions.Start, m.Spec.Actions.Stop} {
		if a == nil || !a.Enabled {
			continue
		}
		enabled = true
		if m.Spec.Type == "cron" && (a.Time != "" || a.Day != nil) {
			return ErrInvalidConfig
		}
		if m.Spec.Type != "cron" && a.Crontab != "" {
			return ErrInvalidConfig
		}
		if m.Spec.Type == "daily" && a.Day != nil {
			return ErrInvalidConfig
		}
		if _, err := schedule.CompileAction(m.Spec.Type, a.Time, a.Crontab, a.Day); err != nil {
			return err
		}
	}
	if !enabled {
		return ErrInvalidConfig
	}
	return nil
}

// Location resolves the mandatory application timezone.
func (c Config) Location() (*time.Location, error) { return time.LoadLocation(c.Timezone.String()) }
