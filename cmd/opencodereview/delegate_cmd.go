package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/open-code-review/open-code-review/internal/config/rules"
	"github.com/open-code-review/open-code-review/internal/gitcmd"
	"github.com/open-code-review/open-code-review/internal/reviewbundle"
	"github.com/open-code-review/open-code-review/internal/stdout"
)

type delegatePrepareOptions struct {
	repoDir        string
	rulePath       string
	from           string
	to             string
	commit         string
	excludes       string
	includes       string
	outputPath     string
	maxBundleBytes int
	maxGitProcs    int
	showHelp       bool
}

func runDelegate(args []string) error {
	return runDelegateWithWriter(args, stdout.Writer())
}

func runDelegateWithWriter(args []string, writer io.Writer) error {
	if len(args) == 0 {
		return executeDelegateMain(nil, writer)
	}
	switch args[0] {
	case "validate":
		return runDelegateValidate(context.Background(), args[1:], writer)
	case "report":
		return runDelegateReport(args[1:], writer)
	case "-h", "--help":
		printDelegateUsage(writer)
		return nil
	default:
		if strings.HasPrefix(args[0], "-") {
			return executeDelegateMain(args, writer)
		}
		return fmt.Errorf("unknown delegate command: %s", args[0])
	}
}

func executeDelegateMain(args []string, writer io.Writer) error {
	options, err := parseDelegatePrepareFlags(args)
	if err != nil {
		return err
	}
	if options.showHelp {
		printDelegatePrepareUsage(writer)
		return nil
	}
	return executeDelegatePrepare(context.Background(), options, writer)
}

func parseDelegatePrepareFlags(args []string) (delegatePrepareOptions, error) {
	flags := newOcrFlagSet("ocr delegate")
	options := delegatePrepareOptions{}
	flags.StringVar(&options.repoDir, "repo", "", "root directory of the git repository")
	flags.StringVar(&options.rulePath, "rule", "", "path to a custom review rule file")
	flags.StringVar(&options.from, "from", "", "source ref for a range review")
	flags.StringVar(&options.to, "to", "", "target ref for a range review")
	flags.StringVarP(&options.commit, "commit", "c", "", "single commit to review")
	flags.StringVar(&options.excludes, "exclude", "", "comma-separated path patterns to exclude")
	flags.StringVar(&options.includes, "include", "", "comma-separated path patterns to include")
	flags.StringVar(&options.outputPath, "output", "", "explicit bundle output path")
	flags.IntVar(
		&options.maxBundleBytes,
		"max-bundle-bytes",
		int(reviewbundle.DefaultMaxBundleBytes),
		"maximum encoded bundle size",
	)
	flags.IntVar(&options.maxGitProcs, "max-git-procs", 16, "maximum concurrent git subprocesses")
	if err := flags.Parse(args); err != nil {
		return options, fmt.Errorf("parse flags: %w", err)
	}
	options.showHelp = flags.showHelp
	if options.showHelp {
		return options, nil
	}
	if err := validateDelegatePrepareOptions(options); err != nil {
		return options, err
	}
	return options, nil
}

func validateDelegatePrepareOptions(options delegatePrepareOptions) error {
	modeCount := 0
	if options.from != "" || options.to != "" {
		modeCount++
	}
	if options.commit != "" {
		modeCount++
	}
	if modeCount > 1 {
		return fmt.Errorf("only one review mode allowed (--from/--to or --commit)")
	}
	if options.from != "" && options.to == "" {
		return fmt.Errorf("--to is required when --from is specified")
	}
	if options.to != "" && options.from == "" {
		return fmt.Errorf("--from is required when --to is specified")
	}
	if options.maxBundleBytes <= 0 {
		return fmt.Errorf("--max-bundle-bytes must be greater than zero")
	}
	if options.maxGitProcs <= 0 {
		return fmt.Errorf("--max-git-procs must be greater than zero")
	}
	return nil
}

func executeDelegatePrepare(
	ctx context.Context,
	options delegatePrepareOptions,
	writer io.Writer,
) error {
	repoDir, _, err := resolveWorkingDir(options.repoDir, true)
	if err != nil {
		return err
	}
	resolver, fileFilter, err := rules.NewResolver(repoDir, options.rulePath)
	if err != nil {
		return fmt.Errorf("load rules: %w", err)
	}
	excludePatterns := splitPaths(options.excludes)
	if len(excludePatterns) > 0 {
		if fileFilter == nil {
			fileFilter = &rules.FileFilter{}
		}
		fileFilter.Exclude = append(fileFilter.Exclude, excludePatterns...)
	}
	includePatterns := splitPaths(options.includes)
	if len(includePatterns) > 0 {
		if fileFilter == nil {
			fileFilter = &rules.FileFilter{}
		}
		fileFilter.Include = append(fileFilter.Include, includePatterns...)
	}

	bundle, encoded, err := reviewbundle.Prepare(ctx, reviewbundle.PrepareOptions{
		RepoDir: repoDir,
		Target: reviewbundle.TargetSpec{
			From: options.from, To: options.to, Commit: options.commit,
		},
		Resolver:      resolver,
		FileFilter:    fileFilter,
		GitRunner:     gitcmd.New(options.maxGitProcs),
		MaxBundleSize: int64(options.maxBundleBytes),
	})
	if err != nil {
		return fmt.Errorf("prepare review bundle: %w", err)
	}

	bundlePath := options.outputPath
	if bundlePath == "" {
		tmpFile, tmpErr := os.CreateTemp("", "ocr-bundle-*.json")
		if tmpErr != nil {
			return fmt.Errorf("create temp file: %w", tmpErr)
		}
		bundlePath = tmpFile.Name()
		_ = tmpFile.Close()
	}
	if err := writePrivateFile(bundlePath, encoded); err != nil {
		return err
	}

	absBundlePath, err := filepath.Abs(bundlePath)
	if err != nil {
		return fmt.Errorf("resolve bundle path: %w", err)
	}
	workflow := renderDelegateWorkflow(bundle, absBundlePath)
	_, err = fmt.Fprint(writer, workflow)
	return err
}

func renderDelegateWorkflow(bundle *reviewbundle.Bundle, bundlePath string) string {
	var sb strings.Builder

	sb.WriteString("# OCR Delegate Review Workflow\n\n")
	sb.WriteString("## Review Target\n\n")
	sb.WriteString(fmt.Sprintf("- **Mode**: %s\n", bundle.Target.Mode))
	sb.WriteString(fmt.Sprintf("- **Files**: %d total, %d reviewable\n",
		bundle.Summary.TotalFiles, bundle.Summary.ReviewableFiles))
	sb.WriteString(fmt.Sprintf("- **Changes**: +%d -%d\n",
		bundle.Summary.Insertions, bundle.Summary.Deletions))
	sb.WriteString(fmt.Sprintf("- **Bundle**: `%s`\n", bundlePath))
	sb.WriteString(fmt.Sprintf("- **Bundle ID**: `%s`\n\n", bundle.BundleID))

	sb.WriteString("## Step 1: Read the Bundle and Produce Review Comments\n\n")
	sb.WriteString("Read the bundle file above. It contains the full review evidence:\n")
	sb.WriteString("file patches, hunks, content hashes, and review rules.\n\n")
	sb.WriteString("Your task: review the code changes and produce a JSON comments file\n")
	sb.WriteString("conforming to the contract below.\n\n")

	sb.WriteString("### Contract\n\n")
	contractJSON, _ := json.MarshalIndent(bundle.Contract, "", "  ")
	sb.WriteString("```json\n")
	sb.WriteString(string(contractJSON))
	sb.WriteString("\n```\n\n")

	sb.WriteString("### Output Schema (review-comments/v1)\n\n")
	sb.WriteString("```json\n")
	sb.WriteString(`{
  "schema_version": "review-comments/v1",
  "bundle_id": "` + bundle.BundleID + `",
  "summary": {
    "files_reviewed": <number of files you reviewed>,
    "issues_found": <number of comments>
  },
  "comments": [
    {
      "path": "<file path from bundle>",
      "start_line": <1-based line in new file>,
      "end_line": <1-based line in new file>,
      "priority": "high|medium|low",
      "category": "bug|security|performance|concurrency|maintainability|test",
      "title": "<short title>",
      "content": "<detailed explanation>",
      "recommendation": "<how to fix>",
      "existing_code": "<optional: current code snippet>",
      "suggestion_code": "<optional: suggested replacement>",
      "confidence": <0.0-1.0>
    }
  ]
}`)
	sb.WriteString("\n```\n\n")

	sb.WriteString("### Rules\n\n")
	if len(bundle.Rules) == 0 {
		sb.WriteString("No custom rules configured. Apply standard review best practices.\n\n")
	} else {
		sb.WriteString("Apply these review rules when analyzing the code:\n\n")
		ruleIDs := make([]string, 0, len(bundle.Rules))
		for id := range bundle.Rules {
			ruleIDs = append(ruleIDs, id)
		}
		sort.Strings(ruleIDs)
		for _, id := range ruleIDs {
			rule := bundle.Rules[id]
			sb.WriteString(fmt.Sprintf("- **%s** (pattern: `%s`): %s\n", id, rule.Pattern, rule.Content))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Step 2: Save Comments\n\n")
	commentsPath := strings.TrimSuffix(bundlePath, ".json") + "-comments.json"
	sb.WriteString(fmt.Sprintf("Save your review comments JSON to:\n`%s`\n\n", commentsPath))

	sb.WriteString("## Step 3: Validate\n\n")
	sb.WriteString("Run the following command to validate your comments against the bundle:\n\n")
	sb.WriteString("```bash\n")
	sb.WriteString(fmt.Sprintf("ocr delegate validate --bundle %q --comments %q\n",
		bundlePath, commentsPath))
	sb.WriteString("```\n\n")
	sb.WriteString("If validation returns `\"valid\": false`, fix the errors and re-run.\n\n")

	sb.WriteString("## Step 4: Generate Report\n\n")
	sb.WriteString("Once validation passes, generate the final report:\n\n")
	sb.WriteString("```bash\n")
	sb.WriteString(fmt.Sprintf("ocr delegate report --bundle %q --comments %q\n",
		bundlePath, commentsPath))
	sb.WriteString("```\n\n")

	return sb.String()
}

func printDelegateUsage(writer io.Writer) {
	fmt.Fprintln(writer, `Usage:
  ocr delegate [options]                 Prepare bundle and emit review workflow
  ocr delegate validate [options]        Validate comments against bundle
  ocr delegate report [options]          Render validated comments as report

Run "ocr delegate -h" for prepare options.
Run "ocr delegate validate -h" for validate options.
Run "ocr delegate report -h" for report options.`)
}

func printDelegatePrepareUsage(writer io.Writer) {
	fmt.Fprintln(writer, `Usage:
  ocr delegate [--repo PATH] [--from REF --to REF | --commit REF]
               [--rule PATH] [--exclude PATTERNS] [--include PATTERNS]
               [--output PATH] [--max-bundle-bytes N] [--max-git-procs N]

Prepares a deterministic review bundle and emits a Markdown workflow
for the host agent to follow. No LLM API key required.`)
}
