// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/alibaba/open-code-review/internal/model"
)

func TestTUIReviewPluginSelection(t *testing.T) {
	opts, err := parseReviewFlags([]string{"--review-plugin", "example-review"})
	if err != nil || opts.reviewPlugin != "example-review" {
		t.Fatalf("flags: %+v %v", opts, err)
	}
	args := tuiReviewArgs(opts, "localhost:1")
	i := slices.Index(args, "--review-plugin")
	if i < 0 || args[i+1] != "example-review" {
		t.Fatalf("subprocess dropped selection: %v", args)
	}
	m := testReviewTUI()
	m.opts.resume = "remembered-session"
	var selections, resumes []string
	m.services.review = func(_ context.Context, opts reviewOptions, _ func(string)) (jsonOutput, error) {
		selections = append(selections, opts.reviewPlugin)
		resumes = append(resumes, opts.resume)
		return jsonOutput{Status: "complete"}, nil
	}
	m.services.preview = func(_ context.Context, opts reviewOptions) (model.Preview, error) {
		selections = append(selections, opts.reviewPlugin)
		resumes = append(resumes, opts.resume)
		return model.Preview{}, nil
	}
	for _, command := range []string{"/review example-review", "/review", "/preview example-review", "/review"} {
		next, cmd := m.command(command)
		m = finishTUIReviewFlow(t, next.(reviewTUIModel), cmd)
		if m.opts.reviewPlugin != "" {
			t.Fatal("selection leaked to later commands")
		}
		if m.opts.resume != "remembered-session" {
			t.Fatal("one-off plugin command cleared the saved resume selection")
		}
	}
	if !reflect.DeepEqual(selections, []string{"example-review", "", "example-review", ""}) {
		t.Fatalf("selections: %v", selections)
	}
	if !reflect.DeepEqual(resumes, []string{"", "remembered-session", "", "remembered-session"}) {
		t.Fatalf("resume selections: %v", resumes)
	}
}
