// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewContributionValidation(t *testing.T) {
	for _, tc := range []struct {
		engine, prompt string
		valid          bool
	}{
		{"behavior-v1", "policy.md", true},
		{"arbitrary-code", "policy.md", false},
		{"behavior-v1", "../policy.md", false},
		{"behavior-v1", "C:/policy.md", false},
		{"behavior-v1", "policy.json", false},
		{"behavior-v1", "", false},
	} {
		m := Manifest{SchemaVersion: 1, ID: "behavior", Name: "Behavior", Version: "1", Review: &ReviewContribution{tc.engine, tc.prompt}}
		data, _ := json.Marshal(m)
		_, err := ParseManifest(data)
		if (err == nil) != tc.valid {
			t.Errorf("%+v: %v", tc, err)
		}
	}
	_, err := ParseManifest([]byte(`{"schema_version":1,"id":"b","name":"B","version":"1","review":{"engine":"behavior-v1","prompt":"policy.md","command":"run"}}`))
	if err == nil {
		t.Fatal("unknown review field accepted")
	}
}

func TestReviewPolicyPackage(t *testing.T) {
	for _, tc := range []struct {
		name, content  string
		present, valid bool
	}{
		{"valid", "Check sourced behavior.\n", true, true},
		{"empty", " \n", true, false},
		{"large", strings.Repeat("a", 64*1024+1), true, false},
		{"encoding", string([]byte{0xff}), true, false},
		{"missing", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			manifest := `{"schema_version":1,"id":"behavior","name":"Behavior","version":"1","review":{"engine":"behavior-v1","prompt":"policy.md"}}`
			if err := os.WriteFile(filepath.Join(dir, ManifestFilename), []byte(manifest), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.present {
				if err := os.WriteFile(filepath.Join(dir, "policy.md"), []byte(tc.content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			m, err := inspectPackage(context.Background(), dir)
			if (err == nil) != tc.valid {
				t.Fatalf("package: %v", err)
			}
			if tc.valid {
				got, err := ReviewPrompt(Installed{Manifest: m, Dir: dir})
				if err != nil || got != tc.content {
					t.Fatalf("policy = %q, %v", got, err)
				}
			}
		})
	}
}
