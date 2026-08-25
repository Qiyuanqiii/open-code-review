// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package model

// Diff represents a single file change in a Git-compatible unified diff.
type Diff struct {
	OldPath          string `json:"old_path"`
	NewPath          string `json:"new_path"`
	Diff             string `json:"diff"`
	NewFileContent   string `json:"new_file_content"`
	IsBinary         bool   `json:"is_binary"`
	IsDeleted        bool   `json:"is_deleted"`
	IsNew            bool   `json:"is_new"`
	IsRenamed        bool   `json:"is_renamed"`
	IsCopied         bool   `json:"is_copied,omitempty"`
	IsReplaced       bool   `json:"is_replaced,omitempty"`
	CopyFromPath     string `json:"copy_from_path,omitempty"`
	CopyFromRevision string `json:"copy_from_revision,omitempty"`
	MovedFromPath    string `json:"moved_from_path,omitempty"`
	MovedToPath      string `json:"moved_to_path,omitempty"`
	Insertions       int64  `json:"insertions"`
	Deletions        int64  `json:"deletions"`
}
