package main

import (
	"fmt"
	"os/exec"
	"strings"
)

func runGitCmd(repoDir string, args ...string) ([]byte, error) {
	fullArgs := append([]string{"-C", repoDir}, args...)
	cmd := exec.Command("git", fullArgs...)
	return cmd.CombinedOutput()
}

// runGitCmdStdout runs git and returns stdout only. Unlike runGitCmd it keeps
// stderr separate, so git notices (permission warnings, deprecations, config
// hints) cannot pollute output that is consumed as data — e.g. the path from
// `git rev-parse --show-toplevel`.
func runGitCmdStdout(repoDir string, args ...string) ([]byte, error) {
	fullArgs := append([]string{"-C", repoDir}, args...)
	cmd := exec.Command("git", fullArgs...)
	return cmd.Output()
}

func getCommitMessage(repoDir, commit string) (string, error) {
	out, err := runGitCmd(repoDir, "log", "-1", "--format=%B", "--end-of-options", commit)
	if err != nil {
		return "", fmt.Errorf("git log failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
