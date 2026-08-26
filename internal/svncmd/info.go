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
	"unicode/utf8"
)

var (
	svnURLUserinfoRE = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/@\s]+@`)
	svnURLQueryRE    = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^\s?#]+)\?[^\s]*`)
	svnAuthUserRE    = regexp.MustCompile(`(?i)(authentication failed for )["'][^"']+["']`)
	svnUsernameRE    = regexp.MustCompile(`(?im)(username\s*[:=]\s*)\S+`)
)

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

// WorkingCopyEntry is the recursive, local-only metadata used to validate a
// working copy and recover copy/move history without spawning one process per
// changed path.
type WorkingCopyEntry struct {
	Path             string
	Kind             string
	Revision         string
	URL              string
	RelativeURL      string
	RepositoryRoot   string
	RepositoryUUID   string
	Root             string
	Schedule         string
	Depth            string
	CopyFromURL      string
	CopyFromRevision string
	MovedFrom        string
	MovedTo          string
}

type infoDocument struct {
	Entries []infoEntry `xml:"entry"`
}

type infoEntry struct {
	Path        string `xml:"path,attr"`
	Kind        string `xml:"kind,attr"`
	Revision    string `xml:"revision,attr"`
	URL         string `xml:"url"`
	RelativeURL string `xml:"relative-url"`
	Repository  struct {
		Root string `xml:"root"`
		UUID string `xml:"uuid"`
	} `xml:"repository"`
	WorkingCopy struct {
		Root             string `xml:"wcroot-abspath"`
		Schedule         string `xml:"schedule"`
		Depth            string `xml:"depth"`
		CopyFromURL      string `xml:"copy-from-url"`
		CopyFromRevision string `xml:"copy-from-rev"`
		MovedFrom        string `xml:"moved-from"`
		MovedTo          string `xml:"moved-to"`
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
	entries, err := ParseInfoEntries(data)
	if err != nil {
		return WorkingCopyInfo{}, err
	}
	entry := entries[0]
	if entry.Root == "" {
		return WorkingCopyInfo{}, fmt.Errorf("svn info did not report a working-copy root")
	}
	return WorkingCopyInfo{
		Root:           entry.Root,
		URL:            entry.URL,
		RepositoryRoot: entry.RepositoryRoot,
		RepositoryUUID: entry.RepositoryUUID,
		RelativeURL:    entry.RelativeURL,
		Revision:       entry.Revision,
	}, nil
}

// ParseInfoEntries parses every entry returned by recursive `svn info --xml`.
func ParseInfoEntries(data []byte) ([]WorkingCopyEntry, error) {
	var doc infoDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse svn info XML: %w", err)
	}
	if len(doc.Entries) == 0 {
		return nil, fmt.Errorf("svn info returned no entries")
	}
	entries := make([]WorkingCopyEntry, 0, len(doc.Entries))
	for _, entry := range doc.Entries {
		entries = append(entries, WorkingCopyEntry{
			Path:             entry.Path,
			Kind:             strings.TrimSpace(entry.Kind),
			Revision:         strings.TrimSpace(entry.Revision),
			URL:              strings.TrimSpace(entry.URL),
			RelativeURL:      strings.TrimSpace(entry.RelativeURL),
			RepositoryRoot:   strings.TrimSpace(entry.Repository.Root),
			RepositoryUUID:   strings.TrimSpace(entry.Repository.UUID),
			Root:             strings.TrimSpace(entry.WorkingCopy.Root),
			Schedule:         strings.TrimSpace(entry.WorkingCopy.Schedule),
			Depth:            strings.TrimSpace(entry.WorkingCopy.Depth),
			CopyFromURL:      strings.TrimSpace(entry.WorkingCopy.CopyFromURL),
			CopyFromRevision: strings.TrimSpace(entry.WorkingCopy.CopyFromRevision),
			MovedFrom:        entry.WorkingCopy.MovedFrom,
			MovedTo:          entry.WorkingCopy.MovedTo,
		})
	}
	return entries, nil
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
		return nil, commandErrorWithDiagnostic(ctx, args, err, stderr.String())
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
		return out, commandErrorWithDiagnostic(ctx, args, err, string(out))
	}
	return out, nil
}

func safeCommandText(text string) string {
	text = svnURLUserinfoRE.ReplaceAllString(text, `${1}<redacted>@`)
	text = svnURLQueryRE.ReplaceAllString(text, `${1}?<redacted>`)
	text = svnAuthUserRE.ReplaceAllString(text, `${1}<redacted>`)
	return svnUsernameRE.ReplaceAllString(text, `${1}<redacted>`)
}

func safeCommandArgs(args []string) string {
	redacted := make([]string, len(args))
	sensitiveValue := false
	for i, arg := range args {
		lower := strings.ToLower(arg)
		if sensitiveValue {
			redacted[i] = "<redacted>"
			sensitiveValue = false
			continue
		}
		switch lower {
		case "--password", "--username", "--config-option":
			redacted[i] = arg
			sensitiveValue = true
			continue
		}
		if strings.HasPrefix(lower, "--password=") || strings.HasPrefix(lower, "--username=") || strings.HasPrefix(lower, "--config-option=") {
			redacted[i] = strings.SplitN(arg, "=", 2)[0] + "=<redacted>"
			continue
		}
		redacted[i] = safeCommandText(arg)
	}
	return strings.Join(redacted, " ")
}

const diagnosticLimit = 2000

func commandErrorWithDiagnostic(ctx context.Context, args []string, err error, diagnostic string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	diagnostic = strings.TrimSpace(safeCommandText(diagnostic))
	guidance := svnFailureGuidance(diagnostic)
	if len(diagnostic) > diagnosticLimit {
		diagnostic = diagnostic[len(diagnostic)-diagnosticLimit:]
		for len(diagnostic) > 0 && !utf8.RuneStart(diagnostic[0]) {
			diagnostic = diagnostic[1:]
		}
		diagnostic = "..." + diagnostic
	}
	if diagnostic != "" {
		return fmt.Errorf("svn %s: %w: %s%s", safeCommandArgs(args), err, diagnostic, guidance)
	}
	return fmt.Errorf("svn %s: %w", safeCommandArgs(args), err)
}

func svnFailureGuidance(diagnostic string) string {
	lower := strings.ToLower(diagnostic)
	switch {
	case strings.Contains(lower, "certificate") || strings.Contains(lower, "e230001"):
		return "; non-interactive certificate validation failed; pre-trust the server certificate in the SVN configuration available to this process"
	case strings.Contains(lower, "authentication") || strings.Contains(lower, "authorization failed") || strings.Contains(lower, "e170001") || strings.Contains(lower, "e215004"):
		return "; non-interactive authentication failed; preconfigure credentials in the SVN authentication cache available to this process"
	default:
		return ""
	}
}
