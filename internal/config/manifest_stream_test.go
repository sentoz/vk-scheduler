package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestStreamOrderingAndDocumentDiagnostics(t *testing.T) {
	directory := t.TempDir()
	for name, text := range map[string]string{"b.yml": strings.Replace(validManifest, "name: example", "name: second", 1), "a.yaml": "---\n\n---\n" + validManifest + "---\n", "ignored.txt": "not YAML"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := CaptureBundle(directory)
	if err != nil {
		t.Fatal(err)
	}
	cloud := CloudConfig{ProjectID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Region: "test-region"}
	schedules, err := ParseBundle(bundle, cloud)
	if err != nil || len(schedules) != 2 || schedules[0].Name != "example" || schedules[1].Name != "second" {
		t.Fatal("stream ordering/empty documents", schedules, err)
	}
	broken := CapturedBundle{Files: []ManifestFile{{Name: "numbered.yaml", Data: []byte("---\n\n---\n" + validManifest + "---\n" + strings.Replace(validManifest, "name: example", "name: bad", 1) + "unknown: true\n")}}}
	_, err = ParseBundle(broken, cloud)
	if err == nil || !strings.Contains(err.Error(), "numbered.yaml document 3") {
		t.Fatalf("wrong document ordinal: %v", err)
	}
	bundle.Files[1].Data = []byte(validManifest)
	if _, err := ParseBundle(bundle, cloud); err == nil {
		t.Fatal("duplicate name across files accepted")
	}
}

func TestExpansionIsSinglePassAtTheDocumentBoundary(t *testing.T) {
	t.Setenv("VK_LABEL", "cost $5")
	text := strings.Replace(validManifest, "  name: example", "  name: example\n  annotations:\n    vk-scheduler/display-name: '${VK_LABEL}'", 1)
	cloud := CloudConfig{ProjectID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Region: "test-region"}
	parse := func(text string) ([]Schedule, error) {
		return ParseBundle(CapturedBundle{Files: []ManifestFile{{Name: "env.yaml", Data: []byte(text)}}}, cloud)
	}
	schedules, err := parse(text)
	if err != nil || schedules[0].DisplayName != "cost $5" {
		t.Fatal("dollar changed during decode", err)
	}
	schedules, err = parse(strings.Replace(text, "${VK_LABEL}", "$${VK_LITERAL}", 1))
	if err != nil || schedules[0].DisplayName != "${VK_LITERAL}" {
		t.Fatal("escaped placeholder was expanded twice", err)
	}
	t.Setenv("VK_MISSING", "")
	if _, err := parse(strings.Replace(validManifest, "11111111-1111-4111-8111-111111111111", "${VK_MISSING}", 1)); !errors.Is(err, ErrInvalidConfig) && !errors.Is(err, ErrInvalidResourceID) {
		t.Fatal("missing required env accepted", err)
	}
	if _, err := parse(strings.Replace(validManifest, "    type: vm", "    type: vm\n    folder_id: invalid", 1)); err == nil {
		t.Fatal("legacy folder identity accepted")
	}
}
