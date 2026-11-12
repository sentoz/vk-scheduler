package config

import (
	"errors"
	"os"
	"testing"

	"github.com/woozymasta/jamle"
	"gopkg.in/yaml.v3"
)

func TestAssigningExpansionCannotMutateEnvironment(t *testing.T) {
	const target = "VK_PLAN_TEST_ASSIGN_TARGET"
	t.Setenv(target, "")
	t.Setenv("VK_PLAN_TEST_NESTED", "${"+target+":=poison}")
	t.Setenv("VK_PLAN_TEST_LEFT", "${"+target+":")
	t.Setenv("VK_PLAN_TEST_RIGHT", "=poison}")
	for _, scalar := range []string{
		"${" + target + ":=poison}",
		"${VK_PLAN_TEST_NESTED}",
		"${VK_PLAN_TEST_LEFT}${VK_PLAN_TEST_RIGHT}",
	} {
		root := &yaml.Node{Kind: yaml.ScalarNode, Value: scalar}
		if err := checkExpansionSafety(root, os.LookupEnv); !errors.Is(err, ErrAssigningExpansion) {
			t.Fatalf("assignment was not rejected: %v", err)
		}
		if os.Getenv(target) != "" {
			t.Fatal("preflight mutated environment")
		}
	}
	var document map[string]any
	if err := jamle.Unmarshal([]byte("value: ${"+target+":-fallback}\n"), &document); err != nil {
		t.Fatal(err)
	}
	if document["value"] != "fallback" {
		t.Fatal("failed candidate affected next decode")
	}
}

func TestReadonlyExpansionRemainsCompatible(t *testing.T) {
	t.Setenv("VK_PLAN_TEST_VALUE", "hello")
	t.Setenv("VK_PLAN_TEST_NESTED", "${VK_PLAN_TEST_VALUE}")
	for _, scalar := range []string{
		"${VK_PLAN_TEST_VALUE}", "${VK_PLAN_TEST_NESTED}",
		"${VK_PLAN_TEST_VALUE:-${VK_PLAN_TEST_NESTED}}", "$${VK_PLAN_TEST_VALUE}",
	} {
		if err := checkScalarExpansion(scalar, os.LookupEnv); err != nil {
			t.Fatal(err)
		}
	}
}
