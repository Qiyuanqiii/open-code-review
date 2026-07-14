package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/open-code-review/open-code-review/internal/reviewbundle"
)

func writePrivateFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open output %s: %w", path, err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return fmt.Errorf("write output %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close output %s: %w", path, err)
	}
	return nil
}

func loadBundleByID(path, bundleID string) (*reviewbundle.Bundle, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read bundle: %w", err)
	}
	bundle, bundleErr := reviewbundle.LoadBundle(bytes.NewReader(content))
	if bundleErr == nil {
		if bundle.BundleID != bundleID {
			return nil, fmt.Errorf(
				"bundle at %q has bundle_id %q, comments require %q",
				path, bundle.BundleID, bundleID,
			)
		}
		return bundle, nil
	}
	manifest, manifestErr := reviewbundle.LoadScanManifest(bytes.NewReader(content))
	if manifestErr != nil {
		return nil, bundleErr
	}
	for index := range manifest.Bundles {
		if manifest.Bundles[index].BundleID == bundleID {
			return &manifest.Bundles[index], nil
		}
	}
	return nil, fmt.Errorf("bundle_id %q is not present in scan manifest", bundleID)
}

func loadBundleAndComments(bundlePath, commentsPath string) (*reviewbundle.Bundle, *reviewbundle.Comments, error) {
	commentsFile, err := os.Open(commentsPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open comments: %w", err)
	}
	comments, loadErr := reviewbundle.LoadComments(commentsFile)
	closeErr := commentsFile.Close()
	if loadErr != nil {
		return nil, nil, loadErr
	}
	if closeErr != nil {
		return nil, nil, fmt.Errorf("close comments: %w", closeErr)
	}
	bundle, err := loadBundleByID(bundlePath, comments.BundleID)
	if err != nil {
		return nil, nil, err
	}
	return bundle, comments, nil
}

func loadValidationResult(path string) (*reviewbundle.ValidationResult, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open validation result: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var result reviewbundle.ValidationResult
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode validation result: %w", err)
	}
	return &result, nil
}
