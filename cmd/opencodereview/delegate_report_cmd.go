package main

import (
	"fmt"
	"io"

	"github.com/open-code-review/open-code-review/internal/reviewbundle"
)

type delegateReportOptions struct {
	bundlePath     string
	commentsPath   string
	validationPath string
	outputPath     string
	format         string
	showHelp       bool
}

func runDelegateReport(args []string, writer io.Writer) error {
	options, err := parseDelegateReportFlags(args)
	if err != nil {
		return err
	}
	if options.showHelp {
		printDelegateReportUsage(writer)
		return nil
	}
	bundle, comments, err := loadBundleAndComments(options.bundlePath, options.commentsPath)
	if err != nil {
		return err
	}
	validation, err := loadValidationResult(options.validationPath)
	if err != nil {
		return err
	}
	report, err := reviewbundle.RenderReport(bundle, comments, reviewbundle.ReportOptions{
		Format:     options.format,
		Validation: validation,
	})
	if err != nil {
		return err
	}
	if options.outputPath != "" {
		return writePrivateFile(options.outputPath, report)
	}
	_, err = writer.Write(report)
	return err
}

func parseDelegateReportFlags(args []string) (delegateReportOptions, error) {
	flags := newOcrFlagSet("ocr delegate report")
	options := delegateReportOptions{}
	flags.StringVar(&options.bundlePath, "bundle", "", "review bundle JSON path")
	flags.StringVar(&options.commentsPath, "comments", "", "review comments JSON path")
	flags.StringVar(&options.validationPath, "validation", "", "optional validation result JSON path")
	flags.StringVar(&options.outputPath, "output", "", "explicit report output path")
	flags.StringVarP(&options.format, "format", "f", "markdown", "markdown, text, or json")
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
	switch options.format {
	case "markdown", "text", "json":
	default:
		return options, fmt.Errorf("--format must be markdown, text, or json")
	}
	return options, nil
}

func printDelegateReportUsage(writer io.Writer) {
	fmt.Fprintln(writer, `Usage:
  ocr delegate report --bundle FILE --comments FILE
                      [--validation FILE] [--format markdown|text|json]
                      [--output FILE]

Renders validated review comments as a formatted report.`)
}
