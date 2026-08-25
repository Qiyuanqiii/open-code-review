// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package diff

import (
	"bytes"
	"fmt"
	"strings"
)

// workspaceFileDiffs renders unversioned files as ordinary new-file Git diff
// sections so every working-copy provider can feed the same parser.
func workspaceFileDiffs(repoDir string, files []string) []string {
	results := make([]string, 0, len(files))
	for _, path := range files {
		content, err := readWorkspaceFileForDiff(repoDir, path)
		if err != nil {
			continue
		}

		var section strings.Builder
		section.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n", path, path))
		section.WriteString("new file mode 100644\n")
		if bytes.IndexByte(content, 0) >= 0 {
			section.WriteString(fmt.Sprintf("Binary files /dev/null and b/%s differ\n", path))
			results = append(results, section.String())
			continue
		}

		lineCount := bytes.Count(content, []byte{'\n'})
		if len(content) > 0 && content[len(content)-1] != '\n' {
			lineCount++
		}
		section.WriteString("--- /dev/null\n")
		section.WriteString(fmt.Sprintf("+++ b/%s\n", path))
		section.WriteString(fmt.Sprintf("@@ -0,0 +1,%d @@\n", lineCount))

		lines := bytes.Split(content, []byte{'\n'})
		if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
			lines = lines[:len(lines)-1]
		}
		for _, line := range lines {
			section.WriteByte('+')
			section.Write(line)
			section.WriteByte('\n')
		}
		results = append(results, section.String())
	}
	return results
}
