// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package svncmd

import (
	"strings"
	"testing"
)

func TestValidateVersion(t *testing.T) {
	for _, version := range []string{"1.7.0\n", "1.14.5-SlikSvn\r\n", "2.0.0 vendor"} {
		if err := ValidateVersion(version); err != nil {
			t.Errorf("ValidateVersion(%q): %v", version, err)
		}
	}
}

func TestValidateVersionRejectsUnsupportedOrMalformed(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "1.6.23", want: "1.7"},
		{input: "", want: "no version"},
		{input: "development", want: "cannot parse"},
		{input: "x.7.0", want: "numeric component"},
		{input: "1.x.0", want: "numeric component"},
	}
	for _, test := range tests {
		err := ValidateVersion(test.input)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("ValidateVersion(%q) error = %v, want %q", test.input, err, test.want)
		}
	}
}
