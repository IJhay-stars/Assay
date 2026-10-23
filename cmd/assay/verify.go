package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/use-assay/assay/internal/attest"
	"github.com/use-assay/assay/internal/bundle"
	"github.com/use-assay/assay/internal/differential"
	"github.com/use-assay/assay/internal/horizon"
	"github.com/use-assay/assay/internal/mechanics"
	"github.com/use-assay/assay/internal/registry"
	"github.com/use-assay/assay/internal/scan"
)

// This file implements `assay verify` (issue #39): re-scan the asset,
// recompute the evidence hash, read the on-chain attestation, and report
// whether they agree — with one command, instead of two commands and a human
// comparing hex by eye.
//
// The outcome classes are distinct, because collapsing them is exactly the
// dishonesty the command exists to prevent:
//
//	agree        the scan, the recomputed hash and the chain all match (exit 0)
//	stale        they match, but the attestation is older than -max-age (exit 2)
//	mismatch     one or more of severity/flags/evidence_hash differ (exit 1),
//	             with the differing fields named
//	absent       no attestation on chain for this asset (exit 3)
//	unverifiable the scan is undetermined, so no verdict is possible (exit 4);
//	             an undetermined scan NEVER reports agreement
//
// Staleness is only a failure when the caller supplies -max-age: without it
// the command has no age to judge against and reports agreement regardless of
// age, because freshness is a policy of the caller, not of the tool.

// verifyOutcome is one of the five terminal states of a verification.
type verifyOutcome string

const (
	outcomeAgree        verifyOutcome = "agree"
	outcomeStale        verifyOutcome = "stale"
	outcomeMismatch     verifyOutcome = "mismatch"
	outcomeAbsent       verifyOutcome = "absent"
	outcomeUnverifiable verifyOutcome = "unverifiable"
)

// exit codes, one per outcome class, so a script can branch on $?.
const (
	exitMismatch     = 1
	exitStale        = 2
	exitAbsent       = 3
	exitUnverifiable = 4
)

// OnChainSafety mirrors the registry's get_safety return: the stored
// attestation for one asset, now including the preimage version so a
// verifier does not have to guess which encoding produced the hash.
type OnChainSafety struct {
	Severity        uint32 `json:"severity"`
	Flags           uint32 `json:"flags"`
	EvidenceHash    string `json:"evidence_hash"`
	AttestedAt      int64  `json:"attested_at"`
	PreimageVersion uint32 `json:"preimage_version"`
}

// registryReader reads the on-chain attestation for one asset. It is an
// interface boundary rather than a concrete call so the outcome table test can
// stub the chain; the production implementation is stellarRegistryReader.
type registryReader func(asset mechanics.Asset) (*OnChainSafety, error)

// verifyScanFunc is the re-scan used by verify. It also returns the Subject the
// report was built from, because the optional differential check must compare
// the independent reading against exactly the flags the report used.
type verifyScanFunc func(ctx context.Context, asset mechanics.Asset) (*mechanics.Report, *mechanics.Subject, error)

// VerifyResult is the full verdict, for both the human line and -json.
type VerifyResult struct {
	Asset   string         `json:"asset"`
	Outcome verifyOutcome  `json:"outcome"`
	Fields  []string       `json:"fields,omitempty"`   // which fields mismatched
	AgeSecs int64          `json:"age_secs,omitempty"` // attestation age, for stale
	MaxAge  int64          `json:"max_age_secs,omitempty"`
	Local   *LocalSide     `json:"local,omitempty"`
	Chain   *OnChainSafety `json:"chain,omitempty"`
	// Differential is present only when -differential was requested. Its
	// State is one of the four states in docs/differential.md; a disagreement
	// makes the command fail even when the hash comparison itself agrees.
	Differential *differential.Result `json:"differential,omitempty"`
	Detail       string               `json:"detail,omitempty"`
}

// LocalSide is the freshly scanned side of the comparison.
type LocalSide struct {
	Severity     uint32   `json:"severity"`
	SeverityName string   `json:"severity_name"`
	Flags        uint32   `json:"flags"`
	EvidenceHash string   `json:"evidence_hash"`
	Checks       []string `json:"checks,omitempty"`
	// Encoding is the preimage encoding EvidenceHash was computed under: the
	// one that reproduced the on-chain hash, or the newest the scan supports
	// when none did.
	Encoding string `json:"encoding,omitempty"`
}

// verifyAsset is the whole comparison. It never reports agreement for an
// undetermined scan: the undetermined check runs before anything else, and
// FromReport would refuse the scan anyway.
//
// The error return is for infrastructure failures (scan errored, chain read
// errored) — situations where no verdict exists at all. Every *verdict* —
// agree, stale, mismatch, absent, unverifiable — is a VerifyResult, not an
// error, because "the attestation disagrees" is a successful verification of
// a bad attestation and a script must be able to distinguish the exit codes.
func verifyAsset(ctx context.Context, asset mechanics.Asset, maxAge int64, now time.Time, doScan verifyScanFunc, read registryReader, differentialRPC string) (*VerifyResult, error) {
	res := &VerifyResult{Asset: asset.String()}

	report, subject, err := doScan(ctx, asset)
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}

	// The differential read is independent of the hash comparison: it compares
	// the scanner's flag reading with a second ledger route, so an attestation
	// mismatch does not prevent it from running and an independent failure does
	// not become agreement. An empty URL means the caller did not opt in.
	if differentialRPC != "" {
		d := differentialResult(ctx, asset, subject, differentialRPC)
		res.Differential = &d
	}

	if report.Undetermined {
		res.Outcome = outcomeUnverifiable
		res.Detail = fmt.Sprintf("scan is undetermined (%s); no verdict is possible",
			strings.Join(report.UndeterminedChecks, ", "))
		return res, nil
	}

	params, err := attest.FromReport(report)
	if err != nil {
		res.Outcome = outcomeUnverifiable
		res.Detail = fmt.Sprintf("scan produced nothing attestable: %v", err)
		return res, nil
	}
	res.Local = &LocalSide{
		Severity:     params.Severity,
		SeverityName: params.SeverityName,
		Flags:        params.Flags,
		EvidenceHash: params.EvidenceHash,
		Checks:       params.Checks,
	}

	chain, err := read(asset)
	if err != nil {
		return nil, fmt.Errorf("read on-chain attestation: %w", err)
	}
	if chain == nil {
		res.Outcome = outcomeAbsent
		res.Detail = "no attestation on chain for this asset"
		return res, nil
	}
	res.Chain = chain

	// The chain stores only the hash, not the encoding it was computed under,
	// and a scan today binds more than the attestations on chain did: every
	// live attestation is v1, every scan is v4. So the hash is compared under
	// each encoding the scan supports, newest first. Each encoding is exactly
	// specified, so a match under any one of them means the scan reproduces
	// the attested evidence under that encoding; it is reported as such.
	res.Local.Encoding = firstLineOf(params.Preimage)
	if !strings.EqualFold(params.EvidenceHash, chain.EvidenceHash) {
		if enc, hash, ok := matchingEncoding(report, chain.EvidenceHash); ok {
			res.Local.EvidenceHash, res.Local.Encoding = hash, enc
			params.EvidenceHash = hash
		}
	}

	// Compare the three attested fields by name, so a mismatch says which
	// field moved instead of a generic "not equal".
	var diff []string
	if params.Severity != chain.Severity {
		diff = append(diff, "severity")
	}
	if params.Flags != chain.Flags {
		diff = append(diff, "flags")
	}
	if !strings.EqualFold(params.EvidenceHash, chain.EvidenceHash) {
		diff = append(diff, "evidence_hash")
	}
	if len(diff) > 0 {
		res.Outcome = outcomeMismatch
		res.Fields = diff
		res.Detail = "on-chain attestation does not match the live scan"
		return res, nil
	}

	age := now.Unix() - chain.AttestedAt
	if age < 0 {
		age = 0
	}
	res.AgeSecs = age

	// Staleness is judged only when the caller supplied an age. Otherwise a
	// matching attestation agrees no matter how old it is.
	if maxAge > 0 && age > maxAge {
		res.Outcome = outcomeStale
		res.MaxAge = maxAge
		res.Detail = fmt.Sprintf("attestation matches but is %d seconds old (max %d)", age, maxAge)
		return res, nil
	}

	res.Outcome = outcomeAgree
	res.Detail = "on-chain attestation matches the live scan"
	return res, nil
}

// matchingEncoding re-renders a report under each older encoding it supports
// and returns the first whose hash equals want.
func matchingEncoding(report *mechanics.Report, want string) (encoding, hash string, ok bool) {
	for _, enc := range []string{"v3", "v2", "v1"} {
		p, err := attest.FromReportAs(report, enc)
		if err != nil {
			continue // the scan does not carry what this encoding binds
		}
		if strings.EqualFold(p.EvidenceHash, want) {
			return firstLineOf(p.Preimage), p.EvidenceHash, true
		}
	}
	return "", "", false
}

func firstLineOf(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// stellarRegistryReader is the production registryReader: it derives the
// asset's SAC id and reads get_safety through Soroban RPC simulation using
// the internal/registry client, falling back to stellar CLI if necessary.
// CONTRACT_ID / NETWORK / RPC_URL come from the environment with the same
// defaults as the Makefile.
func stellarRegistryReader(asset mechanics.Asset) (*OnChainSafety, error) {
	contract := os.Getenv("ASSAY_CONTRACT_ID")
	if contract == "" {
		contract = registry.DefaultContractID
	}
	network := os.Getenv("ASSAY_NETWORK")
	if network == "" {
		network = "testnet"
	}
	rpcURL := os.Getenv("ASSAY_RPC_URL")
	if rpcURL == "" {
		rpcURL = registry.DefaultRPCURL
	}

	sac, err := exec.Command("stellar", "contract", "id", "asset",
		"--asset", asset.Code+":"+asset.Issuer, "--network", network).Output()
	if err != nil {
		return nil, fmt.Errorf("derive SAC id: %w", err)
	}
	sacStr := strings.TrimSpace(string(sac))

	client := registry.New(rpcURL, contract)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	s, err := client.GetSafety(ctx, sacStr)
	if err != nil {
		if errors.Is(err, registry.ErrNotAttested) {
			return nil, nil
		}
		return nil, fmt.Errorf("invoke get_safety: %w", err)
	}
	if s == nil {
		return nil, nil
	}

	return &OnChainSafety{
		Severity:     s.Severity,
		Flags:        s.Flags,
		EvidenceHash: s.EvidenceHash,
		AttestedAt:   int64(s.AttestedAt),
	}, nil
}

// differentialResult re-reads the issuer's flags through Soroban RPC and
// compares them with the two copies the scan already fetched. Those copies are
// reduced with the same reconciliation rule the capability check uses
// (SubjectProvider), so the comparison is against what the report believed,
// not against one arbitrary copy. A Subject that never read flags yields an
// inconclusive state, never zero flags.
func differentialResult(ctx context.Context, asset mechanics.Asset, subject *mechanics.Subject, rpcURL string) differential.Result {
	var statFlags, accountFlags *horizon.Flags
	if subject != nil {
		if subject.Stat != nil {
			f := subject.Stat.Flags
			statFlags = &f
		}
		if subject.Issuer != nil {
			f := subject.Issuer.Flags
			accountFlags = &f
		}
	}
	return differential.Compare(ctx, asset.Code, asset.Issuer,
		differential.NewSubjectProvider(statFlags, accountFlags),
		differential.New(rpcURL))
}

// writeFlags prints a flag reading in a stable order, so agreement and
// disagreement are auditable from the text output without decoding JSON.
func writeFlags(w io.Writer, label string, f *horizon.Flags) {
	_, _ = fmt.Fprintf(w, "  %s: auth_required=%t auth_revocable=%t auth_immutable=%t auth_clawback_enabled=%t\n",
		label, f.AuthRequired, f.AuthRevocable, f.AuthImmutable, f.AuthClawbackEnabled)
}

func writeDifferential(w io.Writer, d *differential.Result) {
	_, _ = fmt.Fprintf(w, "differential: %s\n", d.State)
	if d.Reason != "" {
		_, _ = fmt.Fprintf(w, "  %s\n", d.Reason)
	}
	if d.Primary != nil {
		writeFlags(w, "primary flags", d.Primary)
	}
	if d.PrimaryErr != "" {
		_, _ = fmt.Fprintf(w, "  primary error: %s\n", d.PrimaryErr)
	}
	if d.Secondary != nil {
		writeFlags(w, "soroban rpc flags", d.Secondary)
	}
	if d.SecondaryErr != "" {
		_, _ = fmt.Fprintf(w, "  soroban rpc error: %s\n", d.SecondaryErr)
	}
}

// writeVerifyResult renders one verdict as either JSON or text. Keeping it out
// of runVerify makes every state renderable in a test, including the four
// differential states.
func writeVerifyResult(w io.Writer, res *VerifyResult, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			return fmt.Errorf("verify: write: %w", err)
		}
		return nil
	}

	_, _ = fmt.Fprintf(w, "%s: %s\n", strings.ToUpper(string(res.Outcome)), res.Asset)
	if res.Detail != "" {
		_, _ = fmt.Fprintln(w, res.Detail)
	}
	if len(res.Fields) > 0 {
		_, _ = fmt.Fprintln(w, "differing fields:", strings.Join(res.Fields, ", "))
		if res.Local != nil {
			_, _ = fmt.Fprintf(w, "local: severity=%d flags=%d evidence_hash=%s\n",
				res.Local.Severity, res.Local.Flags, res.Local.EvidenceHash)
		}
		if res.Chain != nil {
			_, _ = fmt.Fprintf(w, "chain: severity=%d flags=%d evidence_hash=%s attested_at=%d preimage_version=%d\n",
				res.Chain.Severity, res.Chain.Flags, res.Chain.EvidenceHash, res.Chain.AttestedAt, res.Chain.PreimageVersion)
		}
	}
	if res.Differential != nil {
		writeDifferential(w, res.Differential)
	}
	return nil
}

// verifyExitCode maps a completed verification to the process status. A
// differential disagreement is a mismatch even when the on-chain hash agreed:
// two ledgers reading the issuer's flags differently is exactly the fault this
// opt-in check exists to surface, and it must never exit 0.
func verifyExitCode(res *VerifyResult) int {
	if res.Differential != nil && res.Differential.State == differential.StateDisagreement {
		return exitMismatch
	}
	switch res.Outcome {
	case outcomeAgree:
		return exitSuccess
	case outcomeStale:
		return exitStale
	case outcomeMismatch:
		return exitMismatch
	case outcomeAbsent:
		return exitAbsent
	case outcomeUnverifiable:
		return exitUnverifiable
	}
	return exitGeneric
}

// verifyBundle checks an offline bundle: it recomputes the digest from the
// archived preimage and verifies every file the manifest binds. It never reads
// the chain and never scans. The exit code distinguishes a valid bundle (0)
// from a tampered or incomplete one (1); a bundle that cannot be opened is an
// error, because no verification happened.
func verifyBundle(path string, asJSON bool, w io.Writer) (int, error) {
	v, err := bundle.VerifyFile(path)
	if err != nil {
		return exitUnverifiable, fmt.Errorf("verify bundle %s: %w", path, err)
	}

	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			return exitGeneric, fmt.Errorf("verify: encode result: %w", err)
		}
	} else if v.OK {
		_, _ = fmt.Fprintf(w, "OK %s\n", v.Asset)
		_, _ = fmt.Fprintf(w, "digest: %s\n", v.ComputedDigest)
		_, _ = fmt.Fprintf(w, "scanner: %s\n", v.Scanner)
		_, _ = fmt.Fprintf(w, "preimage: %s (%s)\n", v.Preimage.Version, v.Preimage.Asset)
		for _, note := range v.Notes {
			_, _ = fmt.Fprintf(w, "note: %s\n", note)
		}
	} else {
		_, _ = fmt.Fprintf(w, "MISMATCH %s\n", v.Asset)
		for _, e := range v.Errors {
			_, _ = fmt.Fprintf(w, "  %s\n", e)
		}
	}

	if !v.OK {
		return exitMismatch, nil
	}
	return exitSuccess, nil
}

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	maxAge := fs.Int64("max-age", 0, "treat an attestation older than this many seconds as stale (0 disables the age check)")
	asJSON := fs.Bool("json", false, "print the verdict as JSON for scripting")
	bundlePath := fs.String("bundle", "",
		"verify an offline attestation bundle instead of a live scan (cannot be combined with -max-age)")
	differentialEnabled := fs.Bool("differential", false,
		"also re-read the issuer's flags through Soroban RPC; disagreement fails verification")
	rpcURL := fs.String("rpc", differential.DefaultURL,
		"Soroban RPC URL used by -differential")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *bundlePath != "" {
		if fs.NArg() != 0 {
			return fmt.Errorf("verify -bundle takes no asset argument (got %q)", fs.Arg(0))
		}
		if *maxAge != 0 {
			return fmt.Errorf("verify -bundle cannot check -max-age: a bundle records the report, not when it was attested")
		}
		if *differentialEnabled {
			return fmt.Errorf("verify -bundle cannot run -differential: a bundle has no live scan to re-derive flags from")
		}
		code, err := verifyBundle(*bundlePath, *asJSON, os.Stdout)
		if err != nil {
			// A bundle that cannot be read or parsed is not a mismatch: no
			// claim could be checked at all. It gets the same exit code as an
			// unverifiable scan so a script never reads "broken archive" as
			// "the attestation disagrees".
			fmt.Fprintln(os.Stderr, "assay:", err)
			os.Exit(exitUnverifiable)
		}
		if code != exitSuccess {
			os.Exit(code)
		}
		return nil
	}
	if fs.NArg() != 1 {
		return usagef("verify takes exactly one asset (CODE-ISSUER)")
	}
	asset, err := scan.ParseAsset(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	differentialRPC := ""
	if *differentialEnabled {
		differentialRPC = *rpcURL
	}
	scanner := scan.New()
	res, verdict := verifyAsset(ctx, asset, *maxAge, time.Now(),
		scanner.ScanWithSubject,
		stellarRegistryReader, differentialRPC)
	if verdict != nil {
		return verdict
	}

	if err := writeVerifyResult(os.Stdout, res, *asJSON); err != nil {
		return err
	}
	if code := verifyExitCode(res); code != exitSuccess {
		os.Exit(code)
	}
	return nil
}
