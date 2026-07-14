package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-code-review/open-code-review/internal/reviewbundle"
)

func initDelegateRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+repo)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	git("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repo, "hello.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "initial")
	return repo
}

func TestDelegateWorkspaceMode(t *testing.T) {
	repo := initDelegateRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "hello.go"), []byte("package main\n\nfunc main() { println(\"hi\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := runDelegateWithWriter([]string{"--repo", repo}, &buf); err != nil {
		t.Fatalf("runDelegateWithWriter: %v", err)
	}

	output := buf.String()
	assertContains(t, output, "# OCR Delegate Review Workflow")
	assertContains(t, output, "bundle_id")
	assertContains(t, output, "ocr delegate validate")
	assertContains(t, output, "ocr delegate report")
	assertContains(t, output, "review-comments/v1")
	assertNotContains(t, output, "codex-")
}

func TestDelegateRangeMode(t *testing.T) {
	repo := initDelegateRepo(t)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+repo)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	base := git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "hello.go"), []byte("package main\n\nfunc main() { println(\"range\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "range change")
	head := git("rev-parse", "HEAD")

	var buf bytes.Buffer
	if err := runDelegateWithWriter([]string{"--repo", repo, "--from", base, "--to", head}, &buf); err != nil {
		t.Fatalf("runDelegateWithWriter: %v", err)
	}

	output := buf.String()
	assertContains(t, output, "range")
	assertContains(t, output, "Bundle ID")
}

func TestDelegateCommitMode(t *testing.T) {
	repo := initDelegateRepo(t)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+repo)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.WriteFile(filepath.Join(repo, "hello.go"), []byte("package main\n\nfunc main() { println(\"commit\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "commit change")
	head := git("rev-parse", "HEAD")

	var buf bytes.Buffer
	if err := runDelegateWithWriter([]string{"--repo", repo, "-c", head}, &buf); err != nil {
		t.Fatalf("runDelegateWithWriter: %v", err)
	}

	output := buf.String()
	assertContains(t, output, "commit")
	assertContains(t, output, "Bundle ID")
}

func TestDelegateOutputFlag(t *testing.T) {
	repo := initDelegateRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "hello.go"), []byte("package main\n\nfunc main() { println(\"output\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(t.TempDir(), "bundle.json")
	var buf bytes.Buffer
	if err := runDelegateWithWriter([]string{"--repo", repo, "--output", outputPath}, &buf); err != nil {
		t.Fatalf("runDelegateWithWriter: %v", err)
	}

	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("output file not created: %v", err)
	}
	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	var bundle reviewbundle.Bundle
	if err := json.Unmarshal(content, &bundle); err != nil {
		t.Fatalf("decode bundle: %v", err)
	}
	if bundle.BundleID == "" {
		t.Fatal("bundle_id is empty")
	}
	if bundle.SchemaVersion != "review-bundle/v1" {
		t.Errorf("schema_version = %q, want review-bundle/v1", bundle.SchemaVersion)
	}
}

func TestDelegateRejectsConflictingTargets(t *testing.T) {
	var buf bytes.Buffer
	err := runDelegateWithWriter([]string{"--from", "main", "--to", "dev", "--commit", "abc"}, &buf)
	if err == nil {
		t.Fatal("expected error for conflicting --from/--to and --commit")
	}
	if !strings.Contains(err.Error(), "only one review mode") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDelegateRejectsFromWithoutTo(t *testing.T) {
	var buf bytes.Buffer
	err := runDelegateWithWriter([]string{"--from", "main"}, &buf)
	if err == nil {
		t.Fatal("expected error for --from without --to")
	}
}

func TestDelegateRejectsToWithoutFrom(t *testing.T) {
	var buf bytes.Buffer
	err := runDelegateWithWriter([]string{"--to", "dev"}, &buf)
	if err == nil {
		t.Fatal("expected error for --to without --from")
	}
}

func TestDelegateValidateRoundtrip(t *testing.T) {
	repo := initDelegateRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "hello.go"), []byte("package main\n\nfunc main() { println(\"validate\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bundlePath := filepath.Join(t.TempDir(), "bundle.json")
	var buf bytes.Buffer
	if err := runDelegateWithWriter([]string{"--repo", repo, "--output", bundlePath}, &buf); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	bundleContent, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	var bundle reviewbundle.Bundle
	if err := json.Unmarshal(bundleContent, &bundle); err != nil {
		t.Fatalf("decode bundle: %v", err)
	}

	comments := reviewbundle.Comments{
		SchemaVersion: "review-comments/v1",
		BundleID:      bundle.BundleID,
		Summary:       reviewbundle.CommentsSummary{FilesReviewed: 1, IssuesFound: 1},
		Comments: []reviewbundle.ReviewComment{
			{
				Path:           "hello.go",
				StartLine:      3,
				EndLine:        3,
				Priority:       "medium",
				Category:       "maintainability",
				Title:          "Use fmt.Println",
				Content:        "Consider using fmt.Println instead of println.",
				Recommendation: "Replace println with fmt.Println.",
				Confidence:     0.8,
			},
		},
	}
	commentsJSON, err := json.MarshalIndent(comments, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	commentsPath := filepath.Join(t.TempDir(), "comments.json")
	if err := os.WriteFile(commentsPath, commentsJSON, 0o644); err != nil {
		t.Fatal(err)
	}

	var validateBuf bytes.Buffer
	if err := runDelegateWithWriter([]string{"validate", "--repo", repo, "--bundle", bundlePath, "--comments", commentsPath}, &validateBuf); err != nil {
		t.Fatalf("validate: %v", err)
	}

	var result reviewbundle.ValidationResult
	if err := json.Unmarshal(validateBuf.Bytes(), &result); err != nil {
		t.Fatalf("decode validation: %v\noutput: %s", err, validateBuf.String())
	}
	if !result.Valid {
		t.Fatalf("expected valid=true, got errors: %+v", result.Errors)
	}
	if result.BundleID != bundle.BundleID {
		t.Errorf("validation bundle_id = %q, want %q", result.BundleID, bundle.BundleID)
	}
}

func TestDelegateReportRoundtrip(t *testing.T) {
	repo := initDelegateRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "hello.go"), []byte("package main\n\nfunc main() { println(\"report\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bundlePath := filepath.Join(t.TempDir(), "bundle.json")
	var buf bytes.Buffer
	if err := runDelegateWithWriter([]string{"--repo", repo, "--output", bundlePath}, &buf); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	bundleContent, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	var bundle reviewbundle.Bundle
	if err := json.Unmarshal(bundleContent, &bundle); err != nil {
		t.Fatalf("decode bundle: %v", err)
	}

	comments := reviewbundle.Comments{
		SchemaVersion: "review-comments/v1",
		BundleID:      bundle.BundleID,
		Summary:       reviewbundle.CommentsSummary{FilesReviewed: 1, IssuesFound: 1},
		Comments: []reviewbundle.ReviewComment{
			{
				Path:           "hello.go",
				StartLine:      3,
				EndLine:        3,
				Priority:       "high",
				Category:       "bug",
				Title:          "Unreachable code",
				Content:        "This is a test comment.",
				Recommendation: "Fix the bug.",
				Confidence:     0.9,
			},
		},
	}
	commentsJSON, err := json.MarshalIndent(comments, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	commentsPath := filepath.Join(t.TempDir(), "comments.json")
	if err := os.WriteFile(commentsPath, commentsJSON, 0o644); err != nil {
		t.Fatal(err)
	}

	var reportBuf bytes.Buffer
	if err := runDelegateWithWriter([]string{"report", "--bundle", bundlePath, "--comments", commentsPath}, &reportBuf); err != nil {
		t.Fatalf("report: %v", err)
	}

	output := reportBuf.String()
	if output == "" {
		t.Fatal("report output is empty")
	}
	assertContains(t, output, "hello.go")
	assertContains(t, output, "Unreachable code")
}

func TestDelegateWorkflowNeutralSchema(t *testing.T) {
	repo := initDelegateRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "hello.go"), []byte("package main\n\nfunc main() { println(\"neutral\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bundlePath := filepath.Join(t.TempDir(), "bundle.json")
	var buf bytes.Buffer
	if err := runDelegateWithWriter([]string{"--repo", repo, "--output", bundlePath}, &buf); err != nil {
		t.Fatalf("runDelegateWithWriter: %v", err)
	}

	output := buf.String()
	assertNotContains(t, output, "codex-")
	assertContains(t, output, "review-comments/v1")

	content, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	var bundle reviewbundle.Bundle
	if err := json.Unmarshal(content, &bundle); err != nil {
		t.Fatalf("decode bundle: %v", err)
	}
	if bundle.SchemaVersion != "review-bundle/v1" {
		t.Errorf("bundle schema_version = %q, want review-bundle/v1", bundle.SchemaVersion)
	}
	if bundle.Contract.CommentSchema != "review-comments/v1" {
		t.Errorf("contract comment_schema = %q, want review-comments/v1", bundle.Contract.CommentSchema)
	}
}

func TestDelegateHelp(t *testing.T) {
	var buf bytes.Buffer
	if err := runDelegateWithWriter([]string{"-h"}, &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContains(t, buf.String(), "Usage:")
}

func TestDelegateValidateHelp(t *testing.T) {
	var buf bytes.Buffer
	if err := runDelegateWithWriter([]string{"validate", "-h"}, &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContains(t, buf.String(), "Usage:")
}

func TestDelegateReportHelp(t *testing.T) {
	var buf bytes.Buffer
	if err := runDelegateWithWriter([]string{"report", "-h"}, &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContains(t, buf.String(), "Usage:")
}

func TestDelegateValidateMissingFlags(t *testing.T) {
	var buf bytes.Buffer
	err := runDelegateWithWriter([]string{"validate", "--bundle", "/tmp/x.json"}, &buf)
	if err == nil {
		t.Fatal("expected error for missing --comments")
	}
}

func TestDelegateReportMissingFlags(t *testing.T) {
	var buf bytes.Buffer
	err := runDelegateWithWriter([]string{"report", "--bundle", "/tmp/x.json"}, &buf)
	if err == nil {
		t.Fatal("expected error for missing --comments")
	}
}

func TestDelegateReportInvalidFormat(t *testing.T) {
	var buf bytes.Buffer
	err := runDelegateWithWriter([]string{"report", "--bundle", "/tmp/x.json", "--comments", "/tmp/c.json", "--format", "xml"}, &buf)
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
}

func TestDelegateUnknownSubcommand(t *testing.T) {
	var buf bytes.Buffer
	err := runDelegateWithWriter([]string{"unknown"}, &buf)
	if err == nil {
		t.Fatal("expected error for unknown subcommand")
	}
	if !strings.Contains(err.Error(), "unknown delegate command") {
		t.Errorf("unexpected error: %v", err)
	}
}

func assertContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("expected output to contain %q, got:\n%s", needle, clipString(haystack, 500))
	}
}

func assertNotContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Errorf("expected output NOT to contain %q, got:\n%s", needle, clipString(haystack, 500))
	}
}

func clipString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
