package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"testing"

	"github.com/use-assay/assay/internal/attest"
	"github.com/use-assay/assay/internal/horizon"
	"github.com/use-assay/assay/internal/scan"
)

// The cliIssuer value is a real-shaped issuer account ID; the classification
// tests wrap errors with it so the messages carry the same issuer-controlled
// text production errors carry.
const cliTestIssuer = "GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN"

func TestExitCodesDefined(t *testing.T) {
	if exitCodeForError(nil) != exitSuccess {
		t.Errorf("nil error should give success (0), got %d", exitCodeForError(nil))
	}
	if exitCodeForError(usagef("unknown command %q", "foo")) != exitUsage {
		t.Errorf("usage error should give %d", exitUsage)
	}
	if exitCodeForError(fmt.Errorf("%w: asset %s", horizon.ErrNotFound, cliTestIssuer)) != exitMissing {
		t.Errorf("not-found error should give %d", exitMissing)
	}
	if exitCodeForError(&url.Error{Op: "Get", URL: "https://horizon.stellar.org", Err: errors.New("connection refused")}) != exitUnavailable {
		t.Errorf("network error should give %d", exitUnavailable)
	}
	if exitCodeForError(fmt.Errorf("attestation: %w", attest.ErrUndetermined)) != exitUnknown {
		t.Errorf("undetermined error should give %d", exitUnknown)
	}
	if exitCodeForError(errors.New("disk full")) != exitGeneric {
		t.Errorf("generic error should give %d", exitGeneric)
	}
}

func TestExitCodePathsComprehensive(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"Success nil", nil, exitSuccess},

		{"Usage no command", usagef("no command given"), exitUsage},
		{"Usage unknown command", usagef("unknown command %q", "foo"), exitUsage},
		{"Usage takes exactly one asset", usagef("scan takes exactly one asset (CODE-ISSUER)"), exitUsage},
		{"Usage wrapped usageError", fmt.Errorf("scan: %w", usagef("scan: %w", flag.ErrHelp)), exitUsage},

		{"Usage invalid asset", fmt.Errorf("scan: %w", scan.ErrBadAsset), exitUsage},
		{"Usage invalid holder", fmt.Errorf("scan: %w", scan.ErrBadHolder), exitUsage},
		// The native asset is an understood refusal rather than a malformed
		// input, but it is still an argument these commands refuse before any
		// scan, so it carries the same exit code. docs/cli.md documents it.
		{"Usage native asset", fmt.Errorf("%w: %q cannot be scanned or attested", scan.ErrNativeAsset, "XLM"), exitUsage},
		{"Usage unknown preimage version", fmt.Errorf("attestation: %w", attest.ErrUnknownPreimageVersion), exitUsage},

		{"Missing not found", fmt.Errorf("horizon: get /assets: %w", horizon.ErrNotFound), exitMissing},

		{"Unavailable url error", &url.Error{Op: "Get", URL: "https://horizon.stellar.org", Err: errors.New("connection refused")}, exitUnavailable},
		{"Unavailable wrapped url error", fmt.Errorf("horizon: get /assets: %w",
			&url.Error{Op: "Get", URL: "https://horizon.stellar.org", Err: errors.New("dial tcp: no route to host")}), exitUnavailable},
		{"Unavailable deadline", fmt.Errorf("scan: %w", context.DeadlineExceeded), exitUnavailable},

		{"Unknown undetermined", fmt.Errorf("attestation: %w", attest.ErrUndetermined), exitUnknown},
		{"Unknown unevaluated", fmt.Errorf("attestation: %w", attest.ErrUnevaluated), exitUnknown},
		{"Unknown inconsistent", fmt.Errorf("attestation: %w", attest.ErrInconsistent), exitUnknown},

		{"Generic unexpected", errors.New("disk full"), exitGeneric},
		// An error nothing typed wraps stays generic whatever its words:
		// text is issuer- and upstream-controlled and classifies nothing.
		{"Generic keyword soup", errors.New("parse: http upstream network not found timeout undetermined"), exitGeneric},
		// A missing helper binary used to read as "not found" because its
		// message contained the words; it is a generic failure, not an
		// asset that does not exist.
		{"Generic missing binary", errors.New(`exec: "stellar": executable file not found in $PATH`), exitGeneric},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCodeForError(tt.err); got != tt.want {
				t.Errorf("exitCodeForError(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

// TestExitCodeAssetKeywordCannotStealTheCode pins the failure mode that
// motivated typed classification (issue #298): a not-found error whose text
// contains words the old substring table matched — "parse", "http",
// "network", "upstream" — must still exit exitMissing. The asset code, the
// issuer's home domain and the upstream's own message are all
// issuer-/upstream-controlled text; none of them may pick the exit code.
func TestExitCodeAssetKeywordCannotStealTheCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{
			// An asset whose CODE is a word the usage rules matched.
			name: "not-found asset coded parse",
			err:  fmt.Errorf("%w: asset parse-%s", horizon.ErrNotFound, cliTestIssuer),
			want: exitMissing,
		},
		{
			// An asset whose CODE is a word the unavailable rules matched.
			name: "not-found asset coded http",
			err:  fmt.Errorf("%w: asset http-%s", horizon.ErrNotFound, cliTestIssuer),
			want: exitMissing,
		},
		{
			// An issuer whose HOME DOMAIN contains "network".
			name: "not-found asset on network.example",
			err:  fmt.Errorf("%w: asset USDC-%s home domain network.example", horizon.ErrNotFound, cliTestIssuer),
			want: exitMissing,
		},
		{
			// The upstream's own message repeats every old keyword.
			name: "upstream error echoing all keywords",
			err:  fmt.Errorf("scan: %w", errors.New("upstream parse of network http not found timeout")),
			want: exitGeneric,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCodeForError(tt.err); got != tt.want {
				t.Errorf("exitCodeForError(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

// TestExitCodeFromDispatch drives the real commands, so the classification is
// pinned where it is used: what run() returns is what main() maps to $?.
func TestExitCodeFromDispatch(t *testing.T) {
	t.Run("no command is a usage error", func(t *testing.T) {
		d, _, _, _ := deps(testReport(), nil)
		err := runWith(nil, d)
		if got := exitCodeForError(err); got != exitUsage {
			t.Errorf("runWith(nil) exit = %d, want %d", got, exitUsage)
		}
	})

	t.Run("unknown command is a usage error", func(t *testing.T) {
		d, _, _, _ := deps(testReport(), nil)
		err := runWith([]string{"frobnicate"}, d)
		if got := exitCodeForError(err); got != exitUsage {
			t.Errorf("runWith(frobnicate) exit = %d, want %d", got, exitUsage)
		}
	})

	t.Run("wrong argument count is a usage error", func(t *testing.T) {
		d, _, _, _ := deps(testReport(), nil)
		err := runWith([]string{"scan"}, d)
		if got := exitCodeForError(err); got != exitUsage {
			t.Errorf("runWith(scan) exit = %d, want %d", got, exitUsage)
		}
	})

	t.Run("undefined flag is a usage error", func(t *testing.T) {
		d, _, _, _ := deps(testReport(), nil)
		err := runWith([]string{"scan", "-not-a-flag", "USDC-" + cliIssuer}, d)
		if got := exitCodeForError(err); got != exitUsage {
			t.Errorf("undefined flag exit = %d, want %d", got, exitUsage)
		}
	})

	t.Run("scan failure keeps its typed code end to end", func(t *testing.T) {
		notFound := fmt.Errorf("%w: asset http-%s", horizon.ErrNotFound, cliIssuer)
		d, _, _, _ := deps(nil, notFound)
		err := runWith([]string{"scan", "http-" + cliIssuer}, d)
		if got := exitCodeForError(err); got != exitMissing {
			t.Errorf("not-found scan exit = %d, want %d", got, exitMissing)
		}
	})
}
