// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package svncmd

import (
	"reflect"
	"testing"
)

func TestParseStatusEdgeMetadata(t *testing.T) {
	data := []byte(`<status><target path=".">
  <entry path="new dir/file.txt"><wc-status item="added" props="modified" revision="7" copied="true" switched="true" tree-conflicted="true" moved-from="old dir/file.txt"/></entry>
  <entry path="old dir/file.txt"><wc-status item="deleted" moved-to="new dir/file.txt"/></entry>
</target></status>`)

	entries, err := ParseStatus(data)
	if err != nil {
		t.Fatalf("ParseStatus: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	got := entries[0]
	if got.Path != "new dir/file.txt" || got.Item != "added" || got.Properties != "modified" || got.Revision != "7" {
		t.Errorf("status identity = %+v", got)
	}
	if !got.Copied || !got.Switched || !got.TreeConflicted || got.MovedFrom != "old dir/file.txt" {
		t.Errorf("edge metadata = %+v", got)
	}
	if entries[1].MovedTo != "new dir/file.txt" {
		t.Errorf("MovedTo = %q", entries[1].MovedTo)
	}
}

func TestParseExternalTargets(t *testing.T) {
	data := []byte(`<properties>
  <target path="C:/work/project"><property name="svn:externals">^/vendor lib/vendor</property></target>
  <target path="C:/work/project/empty"><property name="svn:externals">   </property></target>
  <target path="C:/work/project/other"><property name="custom">value</property></target>
</properties>`)

	got, err := ParseExternalTargets(data)
	if err != nil {
		t.Fatalf("ParseExternalTargets: %v", err)
	}
	want := []string{"C:/work/project"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
}

func TestStatusAndPropertyParsersRejectInvalidXML(t *testing.T) {
	if _, err := ParseStatus([]byte(`<status>`)); err == nil {
		t.Error("ParseStatus accepted invalid XML")
	}
	if _, err := ParseExternalTargets([]byte(`<properties>`)); err == nil {
		t.Error("ParseExternalTargets accepted invalid XML")
	}
}
