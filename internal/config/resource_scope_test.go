package config

import (
	"strings"
	"testing"
	"time"
)

func TestManifestInheritsEffectiveCloudScope(t *testing.T) {
	cleanMetadata(t)
	if _, err := loadText(t, "timezone: UTC\nschedules_dir: ./schedules\ncloud: {}\n"); err == nil {
		t.Fatal("missing global cloud scope accepted")
	}
	t.Setenv("OS_PROJECT_ID", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	t.Setenv("OS_REGION_NAME", "other-region")
	cfg, err := loadText(t, mainConfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"vm", "k8s_v1", "k8s_v2"} {
		t.Run(kind, func(t *testing.T) {
			manifest := strings.Replace(validManifest, "type: vm", "type: "+kind, 1)
			bundle := CapturedBundle{Files: []ManifestFile{{Name: "scope.yaml", Data: []byte(manifest)}}}
			items, err := ParseBundle(bundle, cfg.Cloud)
			if err != nil || len(items) != 1 {
				t.Fatal(items, err)
			}
			target := items[0].Resource
			if target.ProjectID != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" || target.Region != "other-region" {
				t.Fatal("resource did not inherit effective scope", target)
			}
			snapshot, err := CompileSnapshot(1, items, time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			if group, ok := snapshot.Groups[target.Key()]; !ok || group.Resource != target {
				t.Fatal("compiled identity lost inherited scope")
			}
			other := cfg.Cloud
			other.ProjectID, other.Region = "cccccccccccccccccccccccccccccccc", "third-region"
			items, err = ParseBundle(bundle, other)
			if err != nil || len(items) != 1 || items[0].Resource.ProjectID != other.ProjectID || items[0].Resource.Region != other.Region || items[0].Resource.Key() == target.Key() {
				t.Fatal("manifest retained a previous scope", items, err)
			}
		})
	}
}
