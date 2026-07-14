package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/open-code-review/open-code-review/internal/gitcmd"
	"github.com/open-code-review/open-code-review/internal/reviewbundle"
)

type delegateValidateOptions struct {
	repoDir      string
	bundlePath   string
	commentsPath string
	outputPath   string
	maxGitProcs  int
	showHelp     bool
}

func runDelegateValidate(
	ctx context.Context,
	args []string,
	writer io.Writer,
) error {
	options, err := parseDelegateValidateFlags(args)
	if err != nil {
		return err
	}
	if options.showHelp {
		printDelegateValidateUsage(writer)
		return nil
	}
	bundle, comments, err := loadBundleAndComments(options.bundlePath, options.commentsPath)
	if err != nil {
		return err
	}
	repoDir, _, err := resolveWorkingDir(
		options.repoDir,
		bundle.Target.Mode != reviewbundle.TargetScan,
	)
	if err != nil {
		return err
	}
	result := reviewbundle.ValidateComments(
		ctx,
		bundle,
		comments,
		repoDir,
		gitcmd.New(options.maxGitProcs),
	)
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode validation result: %w", err)
	}
	if options.outputPath != "" {
		return writePrivateFile(options.outputPath, append(encoded, '\n'))
	}
	_, err = writer.Write(append(encoded, '\n'))
	return err
}

func parseDelegateValidateFlags(args []string) (delegateValidateOptions, error) {
	flags := newOcrFlagSet("ocr delegate validate")
	options := delegateValidateOptions{}
	flags.StringVar(&options.repoDir, "repo", "", "root directory of the git repository")
	flags.StringVar(&options.bundlePath, "bundle", "", "review bundle JSON path")
	flags.StringVar(&options.commentsPath, "comments", "", "review comments JSON path")
	flags.StringVar(&options.outputPath, "output", "", "explicit validation output path")
	flags.IntVar(&options.maxGitProcs, "max-git-procs", 16, "maximum concurrent git subprocesses")
	if err := flags.Parse(args); err != nil {
		return options, fmt.Errorf("parse flags: %w", err)
	}
	options.showHelp = flags.showHelp
	if options.showHelp {
		return options, nil
	}
	if options.bundlePath == "" || options.commentsPath == "" {
		return options, fmt.Errorf("--bundle and --comments are required")
	}
	if options.maxGitProcs <= 0 {
		return options, fmt.Errorf("--max-git-procs must be greater than zero")
	}
	return options, nil
}

func printDelegateValidateUsage(writer io.Writer) {
	fmt.Fprintln(writer, `Usage:
  ocr delegate validate --bundle FILE --comments FILE
                        [--repo PATH] [--output FILE] [--max-git-procs N]

Validates review comments against the immutable bundle evidence.
Returns a JSON validation result with "valid": true/false.`)
}
