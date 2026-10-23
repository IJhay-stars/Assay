package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"

	"github.com/use-assay/assay/internal/attest"
	"github.com/use-assay/assay/internal/horizon"
	"github.com/use-assay/assay/internal/scan"
)

// Exit codes, documented in docs/cli.md. Scripts and the reproducibility job
// branch on these numbers, so a code's meaning must never change.
const (
	exitSuccess     = 0
	exitGeneric     = 1
	exitUsage       = 2
	exitMissing     = 3
	exitUnavailable = 4
	exitUnknown     = 5
)

// usageError marks an error caused by how the command was invoked rather than
// by what it found: an unknown subcommand, the wrong number of arguments, an
// undefined flag. It carries the exit-code classification structurally, so
// exitCodeForError never has to read the message text — the failure mode this
// replaces was an issuer-controlled or upstream-controlled string deciding
// the exit code by containing a word like "parse" or "not found".
type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

// usagef builds a usage error the way fmt.Errorf builds a plain one.
func usagef(format string, a ...any) error {
	return &usageError{err: fmt.Errorf(format, a...)}
}

// parseFlags parses args and classifies a flag error as a usage error. It is
// the one place flag-set parsing happens, so every command classifies an
// undefined flag or an invalid flag value the same way (exit 2).
//
// flag.ErrHelp is passed through unchanged: the flags package already printed
// the usage text, and asking for help is not a malformed invocation, so it
// keeps the generic-failure classification it has always had.
func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usagef("%s: %w", fs.Name(), err)
	}
	return nil
}

// exitCodeForError maps an error to the process exit code, per docs/cli.md.
//
// Every classification is structural: a sentinel matched with errors.Is, a
// typed error matched with errors.As, or the usageError wrapper. No exit code
// may depend on err.Error() text — error text includes issuer-controlled
// strings (an asset code, a home domain) and upstream messages, so a
// substring rule lets a not-found asset whose code is "PARSE" or "HTTP" exit
// with the usage or unavailable code instead of exitMissing, and a script
// that branches on $? is silently wrong.
func exitCodeForError(err error) int {
	if err == nil {
		return exitSuccess
	}
	var uerr *usageError
	switch {
	case errors.As(err, &uerr):
		return exitUsage
	// Inputs the commands refuse before any scan: a malformed CODE-ISSUER
	// (ErrBadAsset), a malformed holder (ErrBadHolder), the native asset
	// (ErrNativeAsset — an understood refusal rather than a parse error, but
	// still an argument these commands refuse, so it carries the same code;
	// documented in docs/cli.md), and a mistyped -preimage-version.
	case errors.Is(err, scan.ErrBadAsset),
		errors.Is(err, scan.ErrBadHolder),
		errors.Is(err, scan.ErrNativeAsset),
		errors.Is(err, attest.ErrUnknownPreimageVersion):
		return exitUsage
	case errors.Is(err, horizon.ErrNotFound):
		return exitMissing
	case isUpstreamUnavailable(err):
		return exitUnavailable
	case errors.Is(err, attest.ErrUndetermined),
		errors.Is(err, attest.ErrUnevaluated),
		errors.Is(err, attest.ErrInconsistent):
		return exitUnknown
	}
	return exitGeneric
}

// isUpstreamUnavailable reports whether err is a transport-level failure — a
// dial, DNS, or timeout failure reaching a source the scan depends on. Those
// errors are typed (net.Error covers *net.OpError, *url.Error and
// *net.DNSError; a bounded scan or request context reports
// context.DeadlineExceeded), so the classification never inspects message
// text the way the deleted substring table did.
func isUpstreamUnavailable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}
