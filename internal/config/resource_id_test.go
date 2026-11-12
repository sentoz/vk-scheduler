package config

import (
	"errors"
	"strings"
	"testing"
)

func TestResourceIDValidationByType(t *testing.T) {
	const opaque = "3AbCdE4FgHiJ5KlMnOpQrStUvWx"
	const uuid = "abcdef01-2345-6789-abcd-0123456789ab"
	for _, kind := range []string{"vm", "k8s_v1", "k8s_v2"} {
		t.Run(kind, func(t *testing.T) {
			for _, id := range []string{uuid, opaque, strings.ToUpper(uuid), "cluster_01-abc", "", "../cluster", "a/b", "a\\b", "a%2Fb", "a?b", "a#b", "a:b", "a b", " a", "a\n", "a\x00b", "кластер", ".", ".."} {
				target := Resource{Type: kind, ID: id, ProjectID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Region: "test-region"}
				wantValid := id == uuid || kind == "k8s_v2" && (id == opaque || id == strings.ToUpper(uuid) || id == "cluster_01-abc")
				err := target.Validate(target.ProjectID, target.Region)
				if wantValid && err != nil || !wantValid && !errors.Is(err, ErrInvalidResourceID) {
					t.Errorf("ID %q: valid=%v, err=%v", id, wantValid, err)
				}
			}
		})
	}
}

func TestManagedK8sManifestPreservesOpaqueID(t *testing.T) {
	const id = "3AbCdE4FgHiJ5KlMnOpQrStUvWx"
	text := strings.Replace(validManifest, "type: vm", "type: k8s_v2", 1)
	text = strings.Replace(text, "11111111-1111-4111-8111-111111111111", id, 1)
	cloud := CloudConfig{ProjectID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Region: "test-region"}
	items, err := ParseBundle(CapturedBundle{Files: []ManifestFile{{Name: "managed.yaml", Data: []byte(text)}}}, cloud)
	if err != nil || len(items) != 1 {
		t.Fatal("opaque ID rejected by schema or manifest validation", err)
	}
	target := items[0].Resource
	if target.ID != id || !strings.HasSuffix(target.Key(), ":k8s_v2:"+id) {
		t.Fatal("ID case or resource identity was changed")
	}
}
