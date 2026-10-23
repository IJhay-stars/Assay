package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestDocsUsageBlockMatchesBinary pins the usage block at the top of
// docs/cli.md to the binary's own usage text (#300). The block is the first
// thing a reader sees, so a flag added to the CLI must be added there in the
// same change or this test fails.
func TestDocsUsageBlockMatchesBinary(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	docPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "docs", "cli.md")
	doc, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}

	const startMarker = "```\nusage:\n"
	text := string(doc)
	start := strings.Index(text, startMarker)
	if start < 0 {
		t.Fatalf("%s has no fenced usage block", docPath)
	}
	text = text[start+len("```\n"):]
	end := strings.Index(text, "\n```")
	if end < 0 {
		t.Fatalf("%s usage block is not closed", docPath)
	}
	want := text[:end+1]

	var got bytes.Buffer
	usage(&got)
	if got.String() != want {
		t.Fatalf("docs/cli.md usage block has drifted from the binary's usage text\n--- docs/cli.md ---\n%s\n--- usage() ---\n%s", want, got.String())
	}
}
