// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

// Package vcs defines the version-control systems understood by the review
// pipeline.
package vcs

// Kind identifies a repository's version-control system.
type Kind string

const (
	// Unknown identifies a directory that is not a supported working copy.
	Unknown Kind = ""
	// Git identifies a Git working tree.
	Git Kind = "git"
	// Subversion identifies an Apache Subversion working copy.
	Subversion Kind = "svn"
)
