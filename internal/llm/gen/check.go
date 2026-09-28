// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/alibaba/open-code-review/internal/llm"
)

const presetDeclaration = "export const PROVIDER_PRESETS: OcrProviderPreset[] ="

func check(output, kotlinOutput string) error {
	if output == "" || kotlinOutput == "" {
		return fmt.Errorf("-output and -kotlin-output paths are required for -check")
	}
	data, err := os.ReadFile(output)
	if err != nil {
		return fmt.Errorf("read provider presets: %w", err)
	}
	providers := llm.ListProviders()
	if err := checkPresets(data, providers); err != nil {
		return fmt.Errorf("provider presets are inconsistent: %w; run go generate ./internal/llm from the repository root and commit the generated files", err)
	}
	kotlinData, err := os.ReadFile(kotlinOutput)
	if err != nil {
		return fmt.Errorf("read Kotlin provider names: %w", err)
	}
	if err := checkKotlinNames(kotlinData, providers); err != nil {
		return fmt.Errorf("Kotlin provider names are inconsistent: %w; run go generate ./internal/llm from the repository root and commit the generated files", err)
	}
	return nil
}

func checkPresets(data []byte, providers []llm.Provider) error {
	if err := validateModelLists(providers); err != nil {
		return err
	}
	presets, err := decodePresets(data)
	if err != nil {
		return err
	}
	if len(presets) != len(providers) {
		return fmt.Errorf("provider count is %d, want %d", len(presets), len(providers))
	}
	for i, provider := range providers {
		actual := presets[i]
		if actual.Name != provider.Name {
			return fmt.Errorf("provider at index %d is %q, want %q", i, actual.Name, provider.Name)
		}
		if !slices.Equal(actual.Models, provider.Models) {
			return fmt.Errorf("models for provider %q are %v, want %v (including order)", provider.Name, actual.Models, provider.Models)
		}
		for _, field := range []struct{ name, actual, want string }{
			{"displayName", actual.DisplayName, provider.DisplayName},
			{"protocol", actual.Protocol, provider.Protocol},
			{"baseUrl", actual.BaseURL, provider.BaseURL},
			{"authHeader", actual.AuthHeader, provider.AuthHeader},
			{"envVar", actual.EnvVar, provider.EnvVar},
		} {
			if field.actual != field.want {
				return fmt.Errorf("%s for provider %q is %q, want %q", field.name, provider.Name, field.actual, field.want)
			}
		}
		if actual.AmbientAuth != provider.AmbientAuth {
			return fmt.Errorf("ambientAuth for provider %q is %t, want %t", provider.Name, actual.AmbientAuth, provider.AmbientAuth)
		}
	}
	return nil
}

func decodePresets(data []byte) ([]preset, error) {
	_, payload, found := bytes.Cut(data, []byte(presetDeclaration))
	if !found {
		return nil, fmt.Errorf("missing PROVIDER_PRESETS declaration")
	}
	payload = bytes.TrimSuffix(bytes.TrimSpace(payload), []byte(";"))
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var records []map[string]json.RawMessage
	if err := decoder.Decode(&records); err != nil {
		return nil, fmt.Errorf("decode provider presets: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("provider presets must contain exactly one JSON array")
	}
	if records == nil {
		return nil, fmt.Errorf("provider presets must be an array")
	}
	presets := make([]preset, len(records))
	for i, record := range records {
		actual := &presets[i]
		var models []*string
		for _, field := range []struct {
			name        string
			required    bool
			destination any
		}{
			{"name", true, &actual.Name},
			{"displayName", true, &actual.DisplayName},
			{"protocol", true, &actual.Protocol},
			{"baseUrl", true, &actual.BaseURL},
			{"envVar", true, &actual.EnvVar},
			{"models", true, &models},
			{"authHeader", false, &actual.AuthHeader},
			{"ambientAuth", false, &actual.AmbientAuth},
		} {
			value, exists := record[field.name]
			if !exists {
				if field.required {
					return nil, fmt.Errorf("provider at index %d is missing required field %q", i, field.name)
				}
				continue
			}
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return nil, fmt.Errorf("field %q for provider at index %d must not be null", field.name, i)
			}
			if err := json.Unmarshal(value, field.destination); err != nil {
				return nil, fmt.Errorf("decode field %q for provider at index %d: %w", field.name, i, err)
			}
			delete(record, field.name)
		}
		for name := range record {
			return nil, fmt.Errorf("unknown field %q for provider at index %d", name, i)
		}
		actual.Models = make([]string, len(models))
		for j, model := range models {
			if model == nil {
				return nil, fmt.Errorf("model at index %d for provider %q must be a string", j, actual.Name)
			}
			actual.Models[j] = *model
		}
	}
	return presets, nil
}
