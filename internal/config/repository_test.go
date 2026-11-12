package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryExamplesMatchGeneratedSchemas(t *testing.T) {
	cleanMetadata(t)
	t.Setenv("OS_PROJECT_ID", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	t.Setenv("OS_REGION_NAME", "test-region")
	raw, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	documents, err := decodeDocuments(raw)
	if err != nil || len(documents) != 1 {
		t.Fatal("invalid raw config example", err)
	}
	var rawConfig Config
	if err := decodeValidated(documents[0].value, "config.json", &rawConfig); err != nil {
		t.Fatal("config example does not match schema before env injection", err)
	}
	cfg, err := Load("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := CaptureBundle("../../examples/schedules")
	if err != nil {
		t.Fatal(err)
	}
	schedules, err := ParseBundle(bundle, cfg.Cloud)
	if err != nil || len(schedules) != 7 {
		t.Fatal(len(schedules), err)
	}
	if _, err := Load("../../deploy/config.yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err := loadText(t, `{"timezone":"UTC","schedules_dir":"./schedules","cloud":{}}`); err != nil {
		t.Fatal("JSON main config rejected", err)
	}
}

func TestKubernetesProjectedBundlePinsDataRevision(t *testing.T) {
	root := t.TempDir()
	revision := filepath.Join(root, "..revision")
	if err := os.Mkdir(revision, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(revision, "schedule.yaml"), []byte(validManifest), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..revision", filepath.Join(root, "..data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..data/schedule.yaml", filepath.Join(root, "schedule.yaml")); err != nil {
		t.Fatal(err)
	}
	bundle, err := CaptureBundle(root)
	if err != nil || bundle.Revision != revision || len(bundle.Files) != 1 || string(bundle.Files[0].Data) != validManifest {
		t.Fatal("projected volume was not captured from one revision", err)
	}
}
