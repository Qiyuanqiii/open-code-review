// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package plugin

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/alibaba/open-code-review/internal/pathutil"
)

// ReviewPrompt reads the policy from the captured package, including after install.
func ReviewPrompt(p Installed) (string, error) {
	if p.Manifest.Review == nil {
		return "", fmt.Errorf("plugin %q has no review contribution", p.Manifest.ID)
	}
	if err := p.Manifest.Validate(); err != nil {
		return "", err
	}
	root, err := pathutil.CanonicalPath(p.Dir)
	if err != nil {
		return "", err
	}
	name, err := filepath.EvalSymlinks(filepath.Join(p.Dir, filepath.FromSlash(p.Manifest.Review.Prompt)))
	if err != nil {
		return "", err
	}
	if !pathutil.WithinBase(root, name) {
		return "", fmt.Errorf("review prompt escapes the installed package")
	}
	data, err := readFileBounded(name, 64*1024)
	if err != nil {
		return "", fmt.Errorf("read review prompt: %w", err)
	}
	if !utf8.Valid(data) || strings.TrimSpace(string(data)) == "" {
		return "", fmt.Errorf("review prompt must contain UTF-8 text")
	}
	return string(data), nil
}
