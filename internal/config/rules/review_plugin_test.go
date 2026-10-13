// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package rules

import (
	"context"
	"reflect"
	"testing"

	"github.com/alibaba/open-code-review/internal/plugin"
)

func TestReviewPluginIsInvocationScoped(t *testing.T) {
	store := pluginRuleTestHome(t)
	source := t.TempDir()
	writePluginRuleFile(t, source, plugin.ManifestFilename, `{"schema_version":1,"id":"behavior","name":"Behavior","version":"1","review":{"engine":"behavior-v1","prompt":"policy.md"}}`)
	writePluginRuleFile(t, source, "policy.md", "Selected behavior policy.")
	if _, err := store.Install(context.Background(), source, false); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	resolve := func(id string) *pluginResolver {
		t.Helper()
		r, _, err := NewResolver(repo, "", ResolverOptions{ReviewPlugin: id})
		if err != nil {
			t.Fatal(err)
		}
		return r.(*pluginResolver)
	}
	ordinary := resolve("")
	selected := resolve("behavior")
	if ordinary.ReviewPrompt() != "" || selected.ReviewPrompt() != "Selected behavior policy." {
		t.Fatal("selection lost")
	}
	if ordinary.Resolve("main.go") != selected.Resolve("main.go") {
		t.Fatal("policy replaced normal file rules")
	}
	if reflect.DeepEqual(ordinary.CanonicalConfig(), selected.CanonicalConfig()) {
		t.Fatal("selection missing from identity")
	}
	if len(selected.EnabledPlugins()) != 1 || !selected.EnabledPlugins()[0].Enabled {
		t.Fatal("missing per-run snapshot")
	}
	items, err := store.List()
	if err != nil || len(items) != 1 || items[0].Enabled {
		t.Fatalf("saved state changed: %+v %v", items, err)
	}
	if resolve("").ReviewPrompt() != "" {
		t.Fatal("selection persisted")
	}
	if err := store.SetEnabled("behavior", true); err != nil {
		t.Fatal(err)
	}
	if resolve("").ReviewPrompt() != "" {
		t.Fatal("global enable activated a review policy")
	}
	writePluginRuleFile(t, source, "policy.md", "Updated policy.")
	if _, err := store.Update(context.Background(), "behavior"); err != nil {
		t.Fatal(err)
	}
	updated := resolve("behavior")
	if selected.ReviewPrompt() != "Selected behavior policy." || updated.ReviewPrompt() != "Updated policy." {
		t.Fatal("snapshot changed during run")
	}
	if reflect.DeepEqual(selected.CanonicalConfig(), updated.CanonicalConfig()) {
		t.Fatal("changed policy reused old identity")
	}
	installRulePlugin(t, store, "rules-only", []string{"rules.json"}, map[string]string{"rules.json": `{"rules":[]}`}, false)
	for _, id := range []string{"missing", "rules-only"} {
		if _, _, err := NewResolver(repo, "", ResolverOptions{ReviewPlugin: id}); err == nil {
			t.Fatalf("invalid review plugin %q accepted", id)
		}
	}
}
