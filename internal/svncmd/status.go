// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package svncmd

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// StatusEntry is the stable subset of `svn status --xml --verbose` used by
// workspace validation and unversioned-file discovery.
type StatusEntry struct {
	Path           string
	Item           string
	Properties     string
	Revision       string
	Copied         bool
	Switched       bool
	TreeConflicted bool
	MovedFrom      string
	MovedTo        string
}

type statusDocument struct {
	Targets []struct {
		Entries []struct {
			Path   string `xml:"path,attr"`
			Status struct {
				Item           string `xml:"item,attr"`
				Properties     string `xml:"props,attr"`
				Revision       string `xml:"revision,attr"`
				Copied         bool   `xml:"copied,attr"`
				Switched       bool   `xml:"switched,attr"`
				TreeConflicted bool   `xml:"tree-conflicted,attr"`
				MovedFrom      string `xml:"moved-from,attr"`
				MovedTo        string `xml:"moved-to,attr"`
			} `xml:"wc-status"`
		} `xml:"entry"`
	} `xml:"target"`
}

// ParseStatus parses machine-readable working-copy status output.
func ParseStatus(data []byte) ([]StatusEntry, error) {
	var doc statusDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse svn status XML: %w", err)
	}
	var entries []StatusEntry
	for _, target := range doc.Targets {
		for _, entry := range target.Entries {
			entries = append(entries, StatusEntry{
				Path:           entry.Path,
				Item:           strings.TrimSpace(entry.Status.Item),
				Properties:     strings.TrimSpace(entry.Status.Properties),
				Revision:       strings.TrimSpace(entry.Status.Revision),
				Copied:         entry.Status.Copied,
				Switched:       entry.Status.Switched,
				TreeConflicted: entry.Status.TreeConflicted,
				MovedFrom:      entry.Status.MovedFrom,
				MovedTo:        entry.Status.MovedTo,
			})
		}
	}
	return entries, nil
}

type propertiesDocument struct {
	Targets []struct {
		Path       string `xml:"path,attr"`
		Properties []struct {
			Name  string `xml:"name,attr"`
			Value string `xml:",chardata"`
		} `xml:"property"`
	} `xml:"target"`
}

// ParseExternalTargets returns the targets that define a non-empty
// svn:externals property.
func ParseExternalTargets(data []byte) ([]string, error) {
	var doc propertiesDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse svn property XML: %w", err)
	}
	var targets []string
	for _, target := range doc.Targets {
		for _, property := range target.Properties {
			if property.Name == "svn:externals" && strings.TrimSpace(property.Value) != "" {
				targets = append(targets, strings.TrimSpace(target.Path))
				break
			}
		}
	}
	return targets, nil
}
