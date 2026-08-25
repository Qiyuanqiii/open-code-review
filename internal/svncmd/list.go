// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package svncmd

import (
	"encoding/xml"
	"fmt"
	pathpkg "path"
	"sort"
	"strings"
)

type listDocument struct {
	Lists []struct {
		Entries []struct {
			Kind string `xml:"kind,attr"`
			Name string `xml:"name"`
		} `xml:"entry"`
	} `xml:"list"`
}

// ParseList parses the machine-readable output of svn list --xml.
func ParseList(out []byte) ([]string, error) {
	var doc listDocument
	if err := xml.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("parse svn list XML: %w", err)
	}
	seen := make(map[string]struct{})
	for _, list := range doc.Lists {
		for _, entry := range list.Entries {
			if entry.Kind != "file" {
				continue
			}
			name := strings.ReplaceAll(entry.Name, "\\", "/")
			name = pathpkg.Clean(name)
			if name == "." || name == "" || name == ".." || strings.HasPrefix(name, "../") || strings.HasPrefix(name, "/") {
				continue
			}
			seen[name] = struct{}{}
		}
	}
	files := make([]string, 0, len(seen))
	for name := range seen {
		files = append(files, name)
	}
	sort.Strings(files)
	return files, nil
}
