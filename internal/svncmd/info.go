// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

// Package svncmd contains the small, read-only Subversion command surface used
// by OpenCodeReview.
package svncmd

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

var svnURLUserinfoRE = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/@\s]+@`)

// WorkingCopyInfo is the stable subset of `svn info --xml` needed by the
// review pipeline.
type WorkingCopyInfo struct {
	Root           string
	URL            string
	RepositoryRoot string
	RepositoryUUID string
	RelativeURL    string
	Revision       string
}

type infoDocument struct {
	Entries []infoEntry `xml:"entry"`
}

type infoEntry struct {
	Revision    string `xml:"revision,attr"`
	URL         string `xml:"url"`
	RelativeURL string `xml:"relative-url"`
	Repository  struct {
		Root string `xml:"root"`
		UUID string `xml:"uuid"`
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
		RepositoryUUID: strings.TrimSpace(entry.Repository.UUID),
		RelativeURL:    strings.TrimSpace(entry.RelativeURL),
		Revision:       strings.TrimSpace(entry.Revision),
	}, nil
}

// Output runs svn in dir and returns stdout only. Keeping stderr separate from
// machine-readable XML prevents localized warnings from corrupting the data.
func Output(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "svn", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return nil, fmt.Errorf("svn %s: %w: %s", safeCommandText(strings.Join(args, " ")), err, safeCommandText(message))
		}
		return nil, fmt.Errorf("svn %s: %w", safeCommandText(strings.Join(args, " ")), err)
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
			return out, fmt.Errorf("svn %s: %w: %s", safeCommandText(strings.Join(args, " ")), err, safeCommandText(message))
		}
		return out, fmt.Errorf("svn %s: %w", safeCommandText(strings.Join(args, " ")), err)
	}
	return out, nil
}

func safeCommandText(text string) string {
	return svnURLUserinfoRE.ReplaceAllString(text, `${1}<redacted>@`)
}
