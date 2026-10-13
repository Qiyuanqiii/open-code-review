// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package tool

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/alibaba/open-code-review/internal/vcs"
)

const (
	codeSearchMaxCount = 100
	codeSearchTimeout  = 10 * time.Second
)

// CodeSearchProvider performs text search across the repository through the repository provider.
type CodeSearchProvider struct {
	FileReader *FileReader
}

func NewCodeSearch(fr *FileReader) *CodeSearchProvider { return &CodeSearchProvider{FileReader: fr} }

func (p *CodeSearchProvider) Tool() Tool { return CodeSearch }

func (p *CodeSearchProvider) Execute(ctx context.Context, args map[string]any) (string, error) {
	searchText, _ := args["search_text"].(string)
	caseSensitive, _ := args["case_sensitive"].(bool)
	usePerlRegexp, _ := args["use_perl_regexp"].(bool)

	filePatternsIface, _ := args["file_patterns"].([]any)
	var patterns []string
	for _, item := range filePatternsIface {
		if s, ok := item.(string); ok && s != "" {
			if hasTraversalPathComponent(s) {
				return "Error: file_patterns must not contain ..", nil
			}
			patterns = append(patterns, strings.ReplaceAll(s, "\\", "/"))
		}
	}

	if strings.TrimSpace(searchText) == "" {
		return "Error: search_text is blank", nil
	}

	result, err := p.search(ctx, searchText, caseSensitive, usePerlRegexp, patterns)
	if err != nil {
		return "", err
	}
	return result, nil
}

func hasTraversalPathComponent(pattern string) bool {
	path := strings.ReplaceAll(pattern, "\\", "/")
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func (p *CodeSearchProvider) search(parent context.Context, text string, caseSensitive, perlRegexp bool, patterns []string) (string, error) {
	source, content, err := p.FileReader.fileSource()
	if err != nil {
		return "", err
	}
	searchSource, ok := source.(vcs.SearchSource)
	if !ok {
		return "", fmt.Errorf("%w: repository content search", vcs.ErrUnsupported)
	}
	ctx, cancel := context.WithTimeout(parent, codeSearchTimeout)
	defer cancel()
	result, err := searchSource.Search(ctx, content, vcs.SearchQuery{
		Text: text, CaseSensitive: caseSensitive, PerlRegexp: perlRegexp,
		Patterns: patterns, Limit: codeSearchMaxCount,
	})
	if err != nil {
		return "", err
	}
	return formatSearchResult(result), nil
}

func formatSearchResult(result vcs.SearchResult) string {
	if len(result.Matches) == 0 && result.Warning == "" {
		return "No matches found"
	}
	grouped := make(map[string][]vcs.Match)
	var order []string
	for _, match := range result.Matches {
		if _, ok := grouped[match.Path]; !ok {
			order = append(order, match.Path)
		}
		grouped[match.Path] = append(grouped[match.Path], match)
	}
	var sb strings.Builder
	if result.Truncated {
		if result.MatchedFiles > 0 {
			fmt.Fprintf(&sb, "Note: Showing the first %d matches across %d matching files. Some files are partially shown or omitted entirely. Narrow file_patterns to see the rest.\n", codeSearchMaxCount, result.MatchedFiles)
		} else {
			fmt.Fprintf(&sb, "Note: The results have been truncated. Only showing first %d results.\n", codeSearchMaxCount)
		}
	}
	for _, path := range order {
		matches := grouped[path]
		fmt.Fprintf(&sb, "File: %s\nMatch lines: %d\n", path, len(matches))
		for _, match := range matches {
			fmt.Fprintf(&sb, "%d|%s\n", match.Line, match.Text)
		}
		sb.WriteString("\n")
	}
	if result.Warning != "" {
		fmt.Fprintf(&sb, "Warning: %s\n", result.Warning)
	}
	return sb.String()
}
