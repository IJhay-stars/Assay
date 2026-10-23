package attester

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/use-assay/assay/internal/attest"
	"github.com/use-assay/assay/internal/eval"
	"github.com/use-assay/assay/internal/mechanics"
	"github.com/use-assay/assay/internal/sac"
)

// Exit codes of a dry-run pass, as docs/attestation-writer.md specifies.
const (
	// ExitWouldAttest means every asset produced submission-ready arguments.
	ExitWouldAttest = 0
	// ExitRefused means at least one asset was refused (undetermined,
	// unevaluated, stale or inconsistent), and nothing failed operationally.
	ExitRefused = 1
	// ExitOperational means a scan, configuration or derivation failure. Distinct
	// from a refusal, and it wins over one: a pass that could not look at an
	// asset says nothing about whether that asset would have been refused.
	ExitOperational = 2
)

// Status is what a dry-run concluded for one asset.
type Status string

const (
	// StatusAttest means the asset would be attested with Result.Params.
	StatusAttest Status = "attest"
	// StatusRefused means the attest package refused the report. Result.Reason is
	// the refusal verbatim.
	StatusRefused Status = "refused"
	// StatusError means the asset could not be evaluated at all.
	StatusError Status = "error"
)

// Result is the dry-run outcome for one asset.
type Result struct {
	Asset  mechanics.Asset
	SAC    string // empty when it could not be derived
	Status Status
	Params attest.Params // set when Status is StatusAttest
	Reason string        // set when Status is StatusRefused or StatusError
}

// ScanFunc scans one asset. Production wires scan.Scanner.Scan; fixture mode
// and tests replay committed evidence.
type ScanFunc func(context.Context, mechanics.Asset) (*mechanics.Report, error)

// refusals are the attest errors that mean "nothing to attest", as opposed to
// an encoding or engine fault. They map to exit 1, never 2.
var refusals = []error{
	attest.ErrUndetermined,
	attest.ErrUnevaluated,
	attest.ErrStale,
	attest.ErrInconsistent,
}

// DryRun runs one pass over the configured assets: scan, derive attest()
// arguments with attest.FromReport, derive the SAC address for the registry's
// network, and stop. It never signs and has no way to: there is no key in
// scope.
func DryRun(ctx context.Context, cfg *Config, scanFn ScanFunc) []Result {
	network := cfg.RegistryPassphrase()
	assets := cfg.ParsedAssets()
	results := make([]Result, 0, len(assets))
	for _, a := range assets {
		results = append(results, dryRunOne(ctx, a, network, scanFn))
	}
	return results
}

func dryRunOne(ctx context.Context, a mechanics.Asset, network string, scanFn ScanFunc) Result {
	r := Result{Asset: a}
	// The SAC is derived first and independently of the scan: it depends only
	// on the configuration, so a refusal or scan failure still names the
	// address that would have been written.
	addr, err := sac.Address(a.Code, a.Issuer, network)
	if err != nil {
		r.Status, r.Reason = StatusError, "derive SAC address: "+err.Error()
		return r
	}
	r.SAC = addr

	rep, err := scanFn(ctx, a)
	if err != nil {
		r.Status, r.Reason = StatusError, err.Error()
		return r
	}
	params, err := attest.FromReport(rep)
	if err != nil {
		for _, refusal := range refusals {
			if errors.Is(err, refusal) {
				r.Status, r.Reason = StatusRefused, err.Error()
				return r
			}
		}
		r.Status, r.Reason = StatusError, err.Error()
		return r
	}
	r.Status, r.Params = StatusAttest, params
	return r
}

// ExitCode is the exit code for a pass: 2 if anything failed operationally,
// else 1 if anything was refused, else 0.
func ExitCode(results []Result) int {
	code := ExitWouldAttest
	for _, r := range results {
		switch r.Status {
		case StatusError:
			return ExitOperational
		case StatusRefused:
			code = ExitRefused
		}
	}
	if len(results) == 0 {
		return ExitOperational
	}
	return code
}

// FormatResult renders one result as a tab-separated line.
//
// A would-attest line is the asset, its SAC address, then exactly the three
// fields `assay attestation -raw` prints (SEVERITY, FLAGS, EVIDENCE_HASH):
//
//	CODE-ISSUER  SAC  SEVERITY  FLAGS  EVIDENCE_HASH
//
// A refusal or error replaces the triple with its status and the reason
// verbatim, with tabs and line breaks escaped so each asset stays one line:
//
//	CODE-ISSUER  SAC  refused  REASON
//	CODE-ISSUER  SAC|-  error  REASON
func FormatResult(r Result) string {
	addr := r.SAC
	if addr == "" {
		addr = "-"
	}
	if r.Status == StatusAttest {
		return fmt.Sprintf("%s\t%s\t%d\t%d\t%s", r.Asset, addr, r.Params.Severity, r.Params.Flags, r.Params.EvidenceHash)
	}
	return fmt.Sprintf("%s\t%s\t%s\t%s", r.Asset, addr, r.Status, oneLine(r.Reason))
}

var lineEscaper = strings.NewReplacer("\\", `\\`, "\t", `\t`, "\n", `\n`, "\r", `\r`)

func oneLine(s string) string { return lineEscaper.Replace(s) }

// FixtureScan returns a ScanFunc that replays each configured asset's fixture
// subject from cfg.FixturesDir instead of fetching anything, binding the report
// to cfg's scan network the way a live scan binds the network it read. It is
// what makes the CI dry-run hermetic.
func FixtureScan(cfg *Config) ScanFunc {
	subjects := make(map[string]string, len(cfg.Assets))
	for i, a := range cfg.ParsedAssets() {
		subjects[a.String()] = cfg.Assets[i].Fixture
	}
	network := cfg.FixtureScanNetwork()
	return func(ctx context.Context, a mechanics.Asset) (*mechanics.Report, error) {
		subject, ok := subjects[a.String()]
		if !ok {
			return nil, fmt.Errorf("no fixture configured for %s", a)
		}
		s, err := eval.LoadSubject(cfg.FixturesDir, subject)
		if err != nil {
			return nil, fmt.Errorf("load fixture %s: %w", subject, err)
		}
		if s.Asset != a {
			return nil, fmt.Errorf("fixture %s is for %s, not %s", subject, s.Asset, a)
		}
		rep, err := mechanics.NewEngine().Run(ctx, s)
		if err != nil {
			return nil, fmt.Errorf("classify fixture %s: %w", subject, err)
		}
		rep.Network = network
		rep.ScannerBound = true
		return rep, nil
	}
}
