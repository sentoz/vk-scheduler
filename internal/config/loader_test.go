package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const mainConfig = "timezone: Europe/Moscow\nschedules_dir: ./schedules\ncloud:\n  project_id: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n  region: test-region\n"
const validManifest = "apiVersion: scheduler.vk/v1alpha1\nkind: Schedule\nmetadata:\n  name: example\nspec:\n  type: weekly\n  resource:\n    type: vm\n    id: 11111111-1111-4111-8111-111111111111\n  actions:\n    start:\n      enabled: true\n      day: 0\n      time: '09:00'\n"

func loadText(t *testing.T, text string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}
func cleanMetadata(t *testing.T) {
	t.Helper()
	for _, key := range []string{"OS_AUTH_URL", "OS_PROJECT_ID", "OS_REGION_NAME", "OS_USER_DOMAIN_NAME", "OS_INTERFACE"} {
		t.Setenv(key, "")
	}
}

func TestLoadDefaultsAndPresence(t *testing.T) {
	cleanMetadata(t)
	cfg, err := loadText(t, mainConfig)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConcurrentJobs != 5 || !cfg.ValidationResources || cfg.OperationTimeout.Std() != 30*time.Minute || cfg.Cloud.MaxConcurrentReads != 8 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	for _, extra := range []string{"max_concurrent_jobs: 0\n", "shutdown_timeout: 0s\n", "operation_timeout: 0s\n", "validation_interval: 0s\n", "metrics_port: 0\n", "unknown: true\n", "operation_timeout: 1d\n", "timezone: Invalid/Zone\n"} {
		t.Run(extra, func(t *testing.T) {
			if _, err := loadText(t, mainConfig+extra); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestMetadataOverrideAndSafeDecode(t *testing.T) {
	cleanMetadata(t)
	t.Setenv("OS_REGION_NAME", "override")
	cfg, err := loadText(t, mainConfig)
	if err != nil || cfg.Cloud.Region != "override" {
		t.Fatal(err)
	}
	for _, text := range []string{"timezone: UTC\ntimezone: Europe/Moscow\n", "timezone: &zone UTC\nschedules_dir: *zone\n", "<<: {timezone: UTC}\n", mainConfig + "cloud_password: SECRET_CANARY\n", mainConfig + "---\n" + mainConfig} {
		_, err := loadText(t, text)
		if err == nil || strings.Contains(err.Error(), "SECRET_CANARY") {
			t.Fatalf("unsafe result: %v", err)
		}
	}
}

func TestManifestWholeSetValidation(t *testing.T) {
	cloud := CloudConfig{ProjectID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Region: "test-region"}
	parse := func(text string) ([]Schedule, error) {
		return ParseBundle(CapturedBundle{Files: []ManifestFile{{Name: "test.yaml", Data: []byte(text)}}}, cloud)
	}
	items, err := parse(validManifest)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Actions.Start.Day == nil || *items[0].Actions.Start.Day != 0 {
		t.Fatal("Sunday presence lost")
	}
	for name, text := range map[string]string{
		"missing day":                strings.Replace(validManifest, "      day: 0\n", "", 1),
		"invalid day":                strings.Replace(validManifest, "day: 0", "day: 7", 1),
		"manifest project forbidden": strings.Replace(validManifest, "    type: vm", "    type: vm\n    project_id: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 1),
		"manifest region forbidden":  strings.Replace(validManifest, "    type: vm", "    type: vm\n    region: test-region", 1),
		"unknown spec":               strings.Replace(validManifest, "  type: weekly", "  surprise: true\n  type: weekly", 1),
		"legacy config":              strings.Replace(validManifest, "  type: weekly", "  weekly_job: {}\n  type: weekly", 1),
		"no enabled":                 strings.Replace(validManifest, "enabled: true", "enabled: false", 1),
		"duplicate name":             validManifest + "---\n" + validManifest,
		"invalid clock":              strings.Replace(validManifest, "09:00", "24:00", 1),
		"uppercase UUID":             strings.Replace(validManifest, "11111111-1111-4111-8111-111111111111", "AAAAAAAA-1111-4111-8111-111111111111", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parse(text); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
	if items, err := ParseBundle(CapturedBundle{}, cloud); err != nil || len(items) != 0 {
		t.Fatal("empty bundle rejected")
	}
}

func TestLoaderAssignmentCannotPoisonNextReload(t *testing.T) {
	cleanMetadata(t)
	t.Setenv("VK_TEST_DEST", "")
	t.Setenv("VK_TEST_NESTED", "${VK_TEST_DEST:=poison}")
	_, err := loadText(t, mainConfig+"unknown: ${VK_TEST_NESTED}\n")
	if !errors.Is(err, ErrAssigningExpansion) || os.Getenv("VK_TEST_DEST") != "" {
		t.Fatal("environment mutated")
	}
}

func TestSchedulesDirectoryIsRelativeToConfiguration(t *testing.T) {
	cleanMetadata(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(path, []byte(mainConfig), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(directory, "schedules")
	if cfg.SchedulesDir != want {
		t.Fatalf("schedules_dir = %q, want %q", cfg.SchedulesDir, want)
	}
}
