package config

import (
	"strings"
	"testing"
)

func TestManagedK8sConfigurationAndIdentity(t *testing.T) {
	cleanMetadata(t)
	t.Setenv("VK_TEST_MANAGED_ENDPOINT", "https://msk.cloud.vk.ru/infra/container/managed-kubernetes/v1")
	cfg, err := loadText(t, mainConfig+"  managed_k8s_endpoint: ${VK_TEST_MANAGED_ENDPOINT}\n")
	if err != nil || cfg.Cloud.ManagedK8sEndpoint != "https://msk.cloud.vk.ru/infra/container/managed-kubernetes/v1" {
		t.Fatal(cfg, err)
	}
	for _, endpoint := range []string{"http://example.test/v1", "https://user:password@example.test/v1", "https://example.test/v1?token=canary", "https://example.test/v1#fragment"} {
		if _, err := loadText(t, mainConfig+"  managed_k8s_endpoint: "+endpoint+"\n"); err == nil {
			t.Fatal("invalid Managed-k8s endpoint accepted")
		}
	}
	text := strings.Replace(validManifest, "type: vm", "type: k8s_v2", 1)
	items, err := ParseBundle(CapturedBundle{Files: []ManifestFile{{Name: "managed.yaml", Data: []byte(text)}}}, cfg.Cloud)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	managed := items[0].Resource
	magnum := managed
	magnum.Type = "k8s_v1"
	if managed.Key() == magnum.Key() {
		t.Fatal("Managed-k8s and Magnum identities collide")
	}
	for _, kind := range []string{"managed-k8s", "managed_k8s", "unknown"} {
		text := strings.Replace(validManifest, "type: vm", "type: "+kind, 1)
		if _, err := ParseBundle(CapturedBundle{Files: []ManifestFile{{Name: "bad.yaml", Data: []byte(text)}}}, cfg.Cloud); err == nil {
			t.Fatal("unsupported resource type passed schema validation", kind)
		}
	}
}
