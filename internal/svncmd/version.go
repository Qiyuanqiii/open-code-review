// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package svncmd

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

const (
	// MinimumMajor and MinimumMinor identify the oldest supported Subversion
	// client. Version 1.7 introduced the Git-compatible diff mode and copy-as-add
	// behavior used by the workspace provider.
	MinimumMajor = 1
	MinimumMinor = 7
)

// ValidateVersion checks the first version token printed by
// `svn --version --quiet`. Vendor suffixes such as "1.14.5-SlikSvn" are
// accepted, while malformed or unsupported versions fail before any working-
// copy history command runs.
func ValidateVersion(output string) error {
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return fmt.Errorf("svn --version --quiet returned no version")
	}
	parts := strings.Split(fields[0], ".")
	if len(parts) < 2 {
		return fmt.Errorf("cannot parse Subversion version %q", fields[0])
	}
	major, err := numericPrefix(parts[0])
	if err != nil {
		return fmt.Errorf("cannot parse Subversion version %q: %w", fields[0], err)
	}
	minor, err := numericPrefix(parts[1])
	if err != nil {
		return fmt.Errorf("cannot parse Subversion version %q: %w", fields[0], err)
	}
	if major < MinimumMajor || major == MinimumMajor && minor < MinimumMinor {
		return fmt.Errorf("Subversion %s is unsupported; version %d.%d or newer is required", fields[0], MinimumMajor, MinimumMinor)
	}
	return nil
}

func numericPrefix(value string) (int, error) {
	end := 0
	for end < len(value) && unicode.IsDigit(rune(value[end])) {
		end++
	}
	if end == 0 {
		return 0, fmt.Errorf("numeric component %q is empty", value)
	}
	return strconv.Atoi(value[:end])
}
