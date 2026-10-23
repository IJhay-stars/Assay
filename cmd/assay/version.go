package main

import (
	"flag"
	"fmt"
	"io"
	"runtime/debug"
	"strings"

	"github.com/use-assay/assay/internal/attest"
)

// This file implements `assay version` (issue #299): print the scanner
// identity, the preimage encodings, and the build facts a verifier needs
// before it compares hashes. Two runs are only comparable if each records
// which scanner and which encoding produced it — that is the same rule that
// binds attest.ScannerIdentity into the v4 preimage, exposed as a command so
// a verifier can read it out of the binary instead of parsing `go version -m`
// output by hand.

// buildSettings is everything `assay version` prints about the binary itself.
type buildSettings struct {
	// Scanner is attest.ScannerIdentity: the identity the v4 preimage's
	// `scanner` line binds. "devel" for unstamped builds.
	Scanner string
	// Preimage is the newest preimage encoding this build writes. Older
	// encodings are still recognized, for verification.
	Preimage string
	// Encodings lists every preimage encoding this build understands,
	// oldest first.
	Encodings []string
	// Module is the main module path, e.g. github.com/use-assay/assay.
	Module string
	// ModuleVersion is the version the Go toolchain stamped on the main
	// module: a release tag, or "devel"/"(devel)" for an unstamped build.
	ModuleVersion string
	// GoVersion is the toolchain that built the binary.
	GoVersion string
	// VCSRevision is the commit the binary was built from, or "unknown"
	// when the build carried no VCS stamp.
	VCSRevision string
	// VCSModified is "true" or "false" when the tree's state at build time
	// was stamped, "unknown" otherwise.
	VCSModified string
}

// preimageEncodings lists the encodings oldest first. It is derived from the
// attest package's own constants, so a new encoding appears here (and in the
// version output) the moment it exists rather than being remembered twice.
var preimageEncodings = []string{
	attest.PreimageVersion,
	attest.PreimageVersionCheckSet,
	attest.PreimageVersionNetwork,
	attest.PreimageVersionScanner,
}

// readBuildSettings reads the binary's own build info. The scanner identity
// comes from attest.ScannerIdentity (which is internal/version.Version); the
// rest comes from debug.ReadBuildInfo, which the Go toolchain stamps into
// every binary.
func readBuildSettings() buildSettings {
	s := buildSettings{
		Scanner:       attest.ScannerIdentity,
		Preimage:      preimageEncodings[len(preimageEncodings)-1],
		Encodings:     append([]string(nil), preimageEncodings...),
		Module:        "unknown",
		ModuleVersion: "unknown",
		GoVersion:     "unknown",
		VCSRevision:   "unknown",
		VCSModified:   "unknown",
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return s
	}
	if info.Main.Path != "" {
		s.Module = info.Main.Path
	}
	if info.Main.Version != "" {
		s.ModuleVersion = info.Main.Version
	}
	if info.GoVersion != "" {
		s.GoVersion = info.GoVersion
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			s.VCSRevision = setting.Value
		case "vcs.modified":
			s.VCSModified = setting.Value
		}
	}
	return s
}

// writeVersion renders the build settings as aligned `key: value` lines, one
// fact per line, so the output is diffable and greppable in scripts.
func writeVersion(w io.Writer, s buildSettings) error {
	_, err := fmt.Fprintf(w, "scanner:       %s\n"+
		"preimage:      %s\n"+
		"encodings:     %s\n"+
		"module:        %s %s\n"+
		"go:            %s\n"+
		"vcs.revision:  %s\n"+
		"vcs.modified:  %s\n",
		s.Scanner,
		s.Preimage,
		strings.Join(s.Encodings, " "),
		s.Module,
		s.ModuleVersion,
		s.GoVersion,
		s.VCSRevision,
		s.VCSModified,
	)
	return err
}

// runVersion implements the version subcommand. It takes no flags of its
// own beyond the standard -h; everything it prints is a fact about the
// binary, not a choice of the caller.
func runVersion(args []string, d commandDeps) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return usagef("version takes no arguments (got %q)", fs.Arg(0))
	}
	return writeVersion(d.stdout, readBuildSettings())
}
