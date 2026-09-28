// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alibaba/open-code-review/internal/llm"
)

func TestCheckPresets(t *testing.T) {
	providers := []llm.Provider{
		{
			Name: "alpha", DisplayName: "Alpha", Protocol: llm.ProtocolOpenAIChatCompletions,
			BaseURL: "https://example.com/v1", AuthHeader: "Authorization",
			EnvVar: "ALPHA_API_KEY", Models: []string{"default", "other", "default"},
		},
		{
			Name: "beta", DisplayName: "Beta", Protocol: llm.ProtocolAnthropicBedrock,
			AmbientAuth: true,
		},
	}
	const alpha = `{"name":"alpha","displayName":"Alpha","protocol":"openai","baseUrl":"https://example.com/v1","authHeader":"Authorization","envVar":"ALPHA_API_KEY","models":["default","other","default"]}`
	const beta = `{"name":"beta","displayName":"Beta","protocol":"anthropic-bedrock","baseUrl":"","envVar":"","ambientAuth":true,"models":[]}`
	valid := "[" + alpha + "," + beta + "]"
	for _, tc := range []struct {
		name, payload, wantErr string
	}{
		{"matching values", valid, ""},
		{"different whitespace", "[\n  " + alpha + ",\n  " + beta + "\n]", ""},
		{"missing provider", "[" + alpha + "]", "provider count"},
		{"extra provider", "[" + alpha + "," + beta + "," + beta + "]", "provider count"},
		{"renamed provider", strings.Replace(valid, `"name":"alpha"`, `"name":"other"`, 1), "provider at index"},
		{"duplicate provider", "[" + alpha + "," + alpha + "]", "provider at index"},
		{"reordered providers", "[" + beta + "," + alpha + "]", "provider at index"},
		{"changed model", strings.Replace(valid, `"other"`, `"changed"`, 1), `models for provider "alpha"`},
		{"reordered models", strings.Replace(valid, `["default","other","default"]`, `["other","default","default"]`, 1), `models for provider "alpha"`},
		{"removed model duplicate", strings.Replace(valid, `["default","other","default"]`, `["default","other"]`, 1), `models for provider "alpha"`},
		{"null models", strings.Replace(valid, `"models":[]`, `"models":null`, 1), "must not be null"},
		{"missing models", strings.Replace(valid, `,"models":[]`, "", 1), `missing required field "models"`},
		{"null model entry", strings.Replace(valid, `["default","other","default"]`, `["default",null,"default"]`, 1), "must be a string"},
		{"wrong model type", strings.Replace(valid, `["default","other","default"]`, `["default",42,"default"]`, 1), `decode field "models"`},
		{"models object", strings.Replace(valid, `"models":[]`, `"models":{}`, 1), `decode field "models"`},
		{"changed display name", strings.Replace(valid, `"displayName":"Alpha"`, `"displayName":"Changed"`, 1), "displayName"},
		{"changed protocol", strings.Replace(valid, `"protocol":"openai"`, `"protocol":"anthropic"`, 1), "protocol"},
		{"changed URL", strings.Replace(valid, `"baseUrl":"https://example.com/v1"`, `"baseUrl":"https://other.example/v1"`, 1), "baseUrl"},
		{"changed auth header", strings.Replace(valid, `"authHeader":"Authorization"`, `"authHeader":"x-api-key"`, 1), "authHeader"},
		{"changed environment variable", strings.Replace(valid, `"envVar":"ALPHA_API_KEY"`, `"envVar":"OTHER_API_KEY"`, 1), "envVar"},
		{"changed ambient authentication", strings.Replace(valid, `"ambientAuth":true`, `"ambientAuth":false`, 1), "ambientAuth"},
		{"unknown field", strings.Replace(valid, `"name":"alpha"`, `"name":"alpha","unknown":true`, 1), "unknown field"},
		{"wrong case field", strings.Replace(valid, `"name":"alpha"`, `"Name":"alpha"`, 1), `missing required field "name"`},
		{"wrong case extra field", strings.Replace(valid, `"name":"alpha"`, `"name":"alpha","Name":"alpha"`, 1), "unknown field"},
		{"missing empty URL", strings.Replace(valid, `,"baseUrl":""`, "", 1), `missing required field "baseUrl"`},
		{"null empty URL", strings.Replace(valid, `"baseUrl":""`, `"baseUrl":null`, 1), "must not be null"},
		{"missing empty environment variable", strings.Replace(valid, `,"envVar":""`, "", 1), `missing required field "envVar"`},
		{"null empty environment variable", strings.Replace(valid, `"envVar":""`, `"envVar":null`, 1), "must not be null"},
		{"null optional auth header", strings.Replace(valid, `"name":"beta"`, `"name":"beta","authHeader":null`, 1), "must not be null"},
		{"null optional ambient authentication", strings.Replace(valid, `"name":"alpha"`, `"name":"alpha","ambientAuth":null`, 1), "must not be null"},
		{"wrong scalar type", strings.Replace(valid, `"displayName":"Alpha"`, `"displayName":42`, 1), `decode field "displayName"`},
		{"null provider", "[" + alpha + ",null]", `missing required field "name"`},
		{"invalid JSON", "[", "decode provider presets"},
		{"null catalog", "null", "must be an array"},
		{"trailing JSON", valid + " []", "exactly one JSON array"},
		{"trailing invalid content", valid + " invalid", "exactly one JSON array"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte("// Different header formatting is allowed.\n" + presetDeclaration + "\n" + tc.payload + ";\n")
			err := checkPresets(data, providers)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want a diagnostic containing %q", err, tc.wantErr)
			}
		})
	}
	if err := checkPresets([]byte(presetDeclaration+" [];\n"), nil); err != nil {
		t.Fatalf("an empty array must match an empty registry: %v", err)
	}
	if err := checkPresets([]byte("export const OTHER = [];\n"), nil); err == nil || !strings.Contains(err.Error(), "missing PROVIDER_PRESETS declaration") {
		t.Fatalf("missing declaration error = %v", err)
	}
}

func TestCheckRequiresExistingFile(t *testing.T) {
	if err := check(""); err == nil || !strings.Contains(err.Error(), "-output") {
		t.Fatalf("missing output path error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "missing.ts")
	if err := check(path); err == nil || !strings.Contains(err.Error(), "read provider presets") {
		t.Fatalf("missing artifact error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("check must not create a missing artifact: %v", err)
	}
}

func TestCheckDoesNotRewriteArtifact(t *testing.T) {
	valid, err := render(llm.ListProviders())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"matching", valid},
		{"inconsistent", []byte(presetDeclaration + " [];\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "providers.ts")
			if err := os.WriteFile(path, tc.data, 0o644); err != nil {
				t.Fatal(err)
			}
			modified := time.Unix(1600000000, 0)
			if err := os.Chtimes(path, modified, modified); err != nil {
				t.Fatal(err)
			}
			err := check(path)
			if tc.name == "matching" && err != nil {
				t.Fatal(err)
			}
			if tc.name == "inconsistent" && (err == nil || !strings.Contains(err.Error(), "go generate ./internal/llm")) {
				t.Fatalf("mismatch must include the regeneration command: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tc.data) || !info.ModTime().Equal(modified) {
				t.Fatal("check must not rewrite the artifact, even when it is inconsistent")
			}
		})
	}
}
