// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"context"
	"testing"
)

func TestValidateDelegateOptions(t *testing.T) {
	cases := []struct {
		name    string
		opts    delegateOptions
		wantErr bool
	}{
		{"workspace", delegateOptions{format: "text"}, false},
		{"commit", delegateOptions{commit: "abc", format: "text"}, false},
		{"range", delegateOptions{from: "main", to: "dev", format: "text"}, false},
		{"SVN target range", delegateOptions{from: "7", to: "9", svnFromTarget: "https://svn.example.com/repo/trunk@7", svnToTarget: "https://svn.example.com/repo/branches/feature@9", format: "text"}, false},
		{"json format", delegateOptions{format: "json"}, false},
		{"empty format", delegateOptions{}, true},
		{"invalid format", delegateOptions{format: "yaml"}, true},
		{"sarif format not supported by delegate", delegateOptions{format: "sarif"}, true},
		{"from without to", delegateOptions{from: "main"}, true},
		{"to without from", delegateOptions{to: "dev"}, true},
		{"commit and range mixed", delegateOptions{commit: "abc", from: "main", to: "dev"}, true},
		{"one SVN target", delegateOptions{from: "7", to: "9", svnFromTarget: "https://svn.example.com/repo/trunk@7", format: "text"}, true},
		{"SVN targets outside range mode", delegateOptions{commit: "9", svnFromTarget: "https://svn.example.com/repo/trunk@7", svnToTarget: "https://svn.example.com/repo/trunk@9", format: "text"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateDelegateOptions(&c.opts)
			if (err != nil) != c.wantErr {
				t.Errorf("validateDelegateOptions() err = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

func TestDelegateContextReviewMode(t *testing.T) {
	cases := []struct {
		name string
		opts delegateOptions
		want string
	}{
		{"commit", delegateOptions{commit: "abc"}, "commit"},
		{"range", delegateOptions{from: "main", to: "dev"}, "range"},
		{"workspace", delegateOptions{}, "workspace"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dc := &delegateContext{cc: &commonContext{}, opts: c.opts}
			if got := dc.reviewMode(); got != c.want {
				t.Errorf("reviewMode() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestDelegateContextMergeBaseEmptyForNonRange(t *testing.T) {
	dc := &delegateContext{cc: &commonContext{}, opts: delegateOptions{commit: "abc"}}
	if got := dc.mergeBase(context.Background()); got != "" {
		t.Errorf("mergeBase(commit mode) = %q, want empty", got)
	}
	dc = &delegateContext{cc: &commonContext{}, opts: delegateOptions{}}
	if got := dc.mergeBase(context.Background()); got != "" {
		t.Errorf("mergeBase(workspace mode) = %q, want empty", got)
	}
}

func TestDelegateContextResolver(t *testing.T) {
	dc := &delegateContext{cc: &commonContext{}, opts: delegateOptions{}}
	if got := dc.resolver(); got != nil {
		t.Errorf("resolver() = %v, want nil", got)
	}
}
