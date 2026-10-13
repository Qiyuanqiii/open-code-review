// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/agent"
	"github.com/alibaba/open-code-review/internal/config/rules"
	"github.com/alibaba/open-code-review/internal/tool"
)

func TestPreparedReviewPinsDiffRulesAndFileTools(t *testing.T) {
	freshOCRHome(t)
	dir := initTestGitRepo(t)
	gitCommitFile(t, dir, "source.m", "#import <Foundation/Foundation.h>\n", "add Objective-C source")
	cc, err := loadCommonContext(dir, "", "HEAD", 0, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	opts := reviewOptions{commit: "HEAD"}
	ctx := context.Background()
	if err := prepareRepositoryReview(ctx, cc, opts); err != nil {
		t.Fatal(err)
	}
	args := agent.Args{RepoDir: dir, Commit: opts.commit, Repository: cc.Repository, ResolvedReview: cc.Review, SystemRule: cc.Resolver, FileFilter: cc.FileFilter}
	sealed, err := agent.ResolveIdentity(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	gitCommitFile(t, dir, "source.m", "% MATLAB source\nx = 1;\n", "move HEAD")
	reader := &tool.FileReader{RepoDir: dir, Source: cc.Repository, Content: cc.Review.Content}
	content, err := reader.Read(ctx, "source.m")
	if err != nil || !strings.HasPrefix(content, "#import") {
		t.Fatalf("file tool read moved content: %q, %v", content, err)
	}
	detail := cc.Resolver.(rules.DetailResolver).ResolveDetail("source.m")
	if detail.SniffedAs != "objc" {
		t.Fatalf("rule resolver read moved content: %+v", detail)
	}
	args.ResolvedReview = &sealed.Review
	again, err := agent.ResolveIdentity(ctx, args)
	if err != nil || again.Identity != sealed.Identity {
		t.Fatalf("review changed after HEAD moved: %+v, %v", again, err)
	}
	search, err := tool.NewCodeSearch(reader).Execute(ctx, map[string]any{"search_text": "Foundation"})
	if err != nil || !strings.Contains(search, "source.m") {
		t.Fatalf("search read moved content: %q, %v", search, err)
	}
}
