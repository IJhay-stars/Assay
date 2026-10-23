package main

import (
	"bytes"
	"runtime"
	"strings"
	"testing"

	"github.com/use-assay/assay/internal/attest"
)

// TestVersionOutputFormat pins the exact rendering: a verifier's script may
// grep these lines, so the labels and their order are a contract.
func TestVersionOutputFormat(t *testing.T) {
	s := buildSettings{
		Scanner:       "v1.2.3",
		Preimage:      attest.PreimageVersionScanner,
		Encodings:     []string{attest.PreimageVersion, attest.PreimageVersionScanner},
		Module:        "github.com/use-assay/assay",
		ModuleVersion: "v1.2.3",
		GoVersion:     "go1.26.9",
		VCSRevision:   "abc123",
		VCSModified:   "false",
	}
	want := "scanner:       v1.2.3\n" +
		"preimage:      " + attest.PreimageVersionScanner + "\n" +
		"encodings:     " + attest.PreimageVersion + " " + attest.PreimageVersionScanner + "\n" +
		"module:        github.com/use-assay/assay v1.2.3\n" +
		"go:            go1.26.9\n" +
		"vcs.revision:  abc123\n" +
		"vcs.modified:  false\n"

	out := &bytes.Buffer{}
	if err := writeVersion(out, s); err != nil {
		t.Fatalf("writeVersion: %v", err)
	}
	if out.String() != want {
		t.Errorf("writeVersion output =\n%s\nwant\n%s", out.String(), want)
	}
}

// TestVersionReadBuildSettings checks what the reader derives from the
// attestation package and from this binary's own build info. Values stamped
// by the toolchain vary by how the test binary was built, so the assertions
// are on identity and presence rather than on a specific revision.
func TestVersionReadBuildSettings(t *testing.T) {
	s := readBuildSettings()

	if s.Scanner != attest.ScannerIdentity {
		t.Errorf("Scanner = %q, want attest.ScannerIdentity %q", s.Scanner, attest.ScannerIdentity)
	}
	if s.Preimage != attest.PreimageVersionScanner {
		t.Errorf("Preimage = %q, want the newest encoding %q", s.Preimage, attest.PreimageVersionScanner)
	}
	// Every PreimageVersion* constant the attest package exports must be
	// listed; a verifier comparing hashes has to know whether the binary
	// understands the encoding the on-chain hash was made under.
	for _, enc := range []string{
		attest.PreimageVersion,
		attest.PreimageVersionCheckSet,
		attest.PreimageVersionNetwork,
		attest.PreimageVersionScanner,
	} {
		found := false
		for _, e := range s.Encodings {
			if e == enc {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Encodings %v does not list %q", s.Encodings, enc)
		}
	}
	if s.GoVersion != runtime.Version() {
		t.Errorf("GoVersion = %q, want %q", s.GoVersion, runtime.Version())
	}
	if s.VCSRevision == "" || s.VCSModified == "" {
		t.Error("VCSRevision and VCSModified must never be empty; use \"unknown\" instead")
	}
}

// TestVersionDispatch covers the subcommand and both flag spellings: all
// three must print the same identity and exit successfully.
func TestVersionDispatch(t *testing.T) {
	for _, args := range [][]string{
		{"version"},
		{"-version"},
		{"--version"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			d, out, _, _ := deps(nil, nil)
			if err := runWith(args, d); err != nil {
				t.Fatalf("runWith(%v): %v", args, err)
			}
			got := out.String()
			for _, want := range []string{
				"scanner:       " + attest.ScannerIdentity,
				"preimage:      " + attest.PreimageVersionScanner,
				"encodings:     ",
				"module:        ",
				"go:            ",
				"vcs.revision:  ",
				"vcs.modified:  ",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("output missing %q:\n%s", want, got)
				}
			}
		})
	}
}

// TestVersionTakesNoArguments keeps the subcommand strict: an argument is a
// usage error, not something to ignore.
func TestVersionTakesNoArguments(t *testing.T) {
	d, _, _, _ := deps(nil, nil)
	err := runWith([]string{"version", "extra"}, d)
	if err == nil {
		t.Fatal("version with an argument should fail")
	}
	if got := exitCodeForError(err); got != exitUsage {
		t.Errorf("version extra: exit = %d, want %d (usage)", got, exitUsage)
	}
}
