// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package svncmd

import (
	"reflect"
	"testing"
)

func TestParseList(t *testing.T) {
	data := []byte(`<?xml version="1.0"?>
<lists><list path="https://example.com/repo/trunk">
  <entry kind="dir"><name>src</name></entry>
  <entry kind="file"><name>src/z.go</name></entry>
  <entry kind="file"><name>src/a b.go</name></entry>
  <entry kind="file"><name>../outside.go</name></entry>
</list></lists>`)
	got, err := ParseList(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"src/a b.go", "src/z.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseList = %v, want %v", got, want)
	}
}

func TestParseListRejectsInvalidXML(t *testing.T) {
	if _, err := ParseList([]byte(`<lists>`)); err == nil {
		t.Fatal("invalid XML was accepted")
	}
}
