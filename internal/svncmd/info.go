// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

// Package svncmd contains the small, read-only Subversion command surface used
// by OpenCodeReview.
package svncmd

import (
	"context"
	"encoding/xml"
	"fmt"
	"os/exec"
	"strings"
)

// WorkingCopyInfo is the stable subset of `svn info --xml` needed by the
// review pipeline.
type WorkingCopyInfo struct {
	Root           string
	URL            string
	RepositoryRoot string
	RelativeURL    string
}

type infoDocument struct {
	Entries []infoEntry `xml:"entry"`
}

type infoEntry struct {
	URL         string `xml:"url"`
	RelativeURL string `xml:"relative-url"`
	Repository  struct {
		Root string `xml:"root"`
	} `xml:"repository"`
	WorkingCopy struct {
		Root string `xml:"wcroot-abspath"`
	} `xml:"wc-info"`
}

// Info reads working-copy metadata without contacting the repository.
func Info(ctx context.Context, dir string) (WorkingCopyInfo, error) {
	out, err := Output(ctx, dir, "info", "--xml", "--depth", "empty", ".")
	if err != nil {
		return WorkingCopyInfo{}, err
	}
	return ParseInfo(out)
}

// ParseInfo parses the machine-readable output of `svn info --xml`.
func ParseInfo(data []byte) (WorkingCopyInfo, error) {
	var doc infoDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return WorkingCopyInfo{}, fmt.Errorf("parse svn info XML: %w", err)
	}
	if len(doc.Entries) == 0 {
		return WorkingCopyInfo{}, fmt.Errorf("svn info returned no entries")
	}
	entry := doc.Entries[0]
	root := strings.TrimSpace(entry.WorkingCopy.Root)
	if root == "" {
		return WorkingCopyInfo{}, fmt.Errorf("svn info did not report a working-copy root")
	}
	return WorkingCopyInfo{
		Root:           root,
		URL:            strings.TrimSpace(entry.URL),
		RepositoryRoot: strings.TrimSpace(entry.Repository.Root),
		RelativeURL:    strings.TrimSpace(entry.RelativeURL),
	}, nil
}

// Output runs svn in dir and returns stdout only. Keeping stderr separate from
// machine-readable XML prevents localized warnings from corrupting the data.
func Output(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "svn", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("svn %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// CombinedOutput runs svn in dir and returns stdout and stderr together. It is
// intended for human-facing command errors and diff output, never XML.
func CombinedOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "svn", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message != "" {
			return out, fmt.Errorf("svn %s: %w: %s", strings.Join(args, " "), err, message)
		}
		return out, fmt.Errorf("svn %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}
