package mechanics_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/use-assay/assay/internal/horizon"
	"github.com/use-assay/assay/internal/mechanics"
	"github.com/use-assay/assay/internal/sep1"
	"github.com/use-assay/assay/internal/stellarexpert"
)

const (
	domainIssuer = "GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN"
	otherIssuer  = "GDHU6WRG4IEQXM5NZ4BMPKOXHW76MZM4Y2IEMFDVXBSDP6SJY4ITNPP2"
)

// domainSubject builds the subject DomainCheck reasons over: a USDC issuer with
// a resolvable home_domain and a stellar.toml whose body the caller supplies.
// The document is parsed from TOML bytes rather than constructed, so the test
// exercises the same decoder the fetcher uses.
func domainSubject(t *testing.T, tomlBody string) *mechanics.Subject {
	t.Helper()
	doc, err := sep1.Parse([]byte(tomlBody))
	if err != nil {
		t.Fatalf("parse stellar.toml: %v", err)
	}
	doc.URL = "https://circle.com/.well-known/stellar.toml"
	doc.FetchedAt = time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)

	return &mechanics.Subject{
		Asset:     mechanics.Asset{Code: "USDC", Issuer: domainIssuer},
		Issuer:    &horizon.Account{AccountID: domainIssuer, HomeDomain: "circle.com"},
		Toml:      doc,
		TomlURL:   sep1.URLFor("circle.com"),
		ScannedAt: time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC),
	}
}

func runDomain(t *testing.T, s *mechanics.Subject) mechanics.Finding {
	t.Helper()
	f, err := mechanics.DomainCheck{}.Run(context.Background(), s)
	if err != nil {
		t.Fatalf("DomainCheck.Run: %v", err)
	}
	return f
}

// TestDomainCheckHedgesOnLinkedEntryWithACode is the bug this fixes. A
// stellar.toml may carry an entry with both a toml link and a code that is not
// this asset. That entry points at a per-currency document Assay never read, so
// the domain may have claimed the asset there; the check must say "unconfirmed"
// rather than the flat "has not claimed this asset". Before the fix the entry
// was skipped by the count, and the unearned negative was emitted.
func TestDomainCheckHedgesOnLinkedEntryWithACode(t *testing.T) {
	f := runDomain(t, domainSubject(t, `
[[CURRENCIES]]
toml = "https://circle.com/.well-known/EURC.toml"
code = "EURC"
issuer = "`+otherIssuer+`"
`))

	if f.Accountability == nil {
		t.Fatal("domain check must always set accountability")
	}
	if *f.Accountability != mechanics.AccountabilityUnverified {
		t.Errorf("accountability = %q, want %q", *f.Accountability, mechanics.AccountabilityUnverified)
	}
	if f.Mechanics&mechanics.MechDomainUnverified == 0 {
		t.Error("the unverified bit must be set")
	}

	// The hedge wording must be present...
	for _, want := range []string{
		"is not declared inline",
		"may be claimed in one of them",
		"because it is unconfirmed, not because it was refuted",
	} {
		if !strings.Contains(f.Reasoning, want) {
			t.Errorf("hedged reasoning is missing %q:\n%s", want, f.Reasoning)
		}
	}
	// ...and the flat negative it replaces must not be, since the whole point
	// is that the domain may yet have claimed the asset.
	if strings.Contains(f.Reasoning, "has not claimed this asset") {
		t.Errorf("linked entry produced the unearned negative:\n%s", f.Reasoning)
	}

	if len(f.Evidence) != 1 {
		t.Fatalf("want exactly one evidence entry, got %d", len(f.Evidence))
	}
	if !strings.Contains(f.Evidence[0].Claim, "1 are links not followed") {
		t.Errorf("evidence claim must state the unfollowed link count: %q", f.Evidence[0].Claim)
	}
}

// TestDomainCheckLinkedEntryMatchingInlineIsVerified is the other half of the
// semantics: an entry that already matches inline IS the claim, so its link is
// irrelevant. It must be counted as unresolved neither for the asset it
// declares nor produce the hedge wording for that asset.
func TestDomainCheckLinkedEntryMatchingInlineIsVerified(t *testing.T) {
	s := domainSubject(t, `
[[CURRENCIES]]
toml = "https://circle.com/.well-known/USDC.toml"
code = "USDC"
issuer = "`+domainIssuer+`"
`)

	if got := s.Toml.LinkedCurrencies(s.Asset.Code, s.Asset.Issuer); got != 0 {
		t.Errorf("a matching inline entry must not count as unresolved: got %d", got)
	}

	f := runDomain(t, s)
	if f.Accountability == nil {
		t.Fatal("domain check must always set accountability")
	}
	if *f.Accountability != mechanics.AccountabilityVerified {
		t.Errorf("accountability = %q, want %q", *f.Accountability, mechanics.AccountabilityVerified)
	}
	if f.Mechanics&mechanics.MechDomainUnverified != 0 {
		t.Error("a verified reciprocal claim must not set the unverified bit")
	}
	if strings.Contains(f.Reasoning, "may be claimed in one of them") {
		t.Errorf("verified entry still hedged:\n%s", f.Reasoning)
	}
	if !strings.Contains(f.Reasoning, "reciprocal") {
		t.Errorf("verified reasoning must state the round trip:\n%s", f.Reasoning)
	}
}

// TestDomainCheckSurfacesCurrencyStatusAsEvidence is the status change: when
// the matching CURRENCIES entry publishes a status, the finding carries it as
// a second attributed evidence claim — and moves nothing else. Severity stays
// Clear (status is an issuer's self-published claim, not a ledger capability)
// and accountability stays Verified (reciprocity already settled it, whatever
// the entry says).
func TestDomainCheckSurfacesCurrencyStatusAsEvidence(t *testing.T) {
	f := runDomain(t, domainSubject(t, `
[[CURRENCIES]]
code = "USDC"
issuer = "`+domainIssuer+`"
status = "dead"
`))

	if f.Severity != mechanics.Clear {
		t.Errorf("severity = %v, want Clear: a published status must never move severity", f.Severity)
	}
	if f.Accountability == nil {
		t.Fatal("domain check must always set accountability")
	}
	if *f.Accountability != mechanics.AccountabilityVerified {
		t.Errorf("accountability = %q, want %q: an issuer calling its own asset dead "+
			"does not un-claim it", *f.Accountability, mechanics.AccountabilityVerified)
	}
	if f.Mechanics&mechanics.MechDomainUnverified != 0 {
		t.Error("a status-carrying claim is still a verified claim; the unverified bit must stay unset")
	}

	if len(f.Evidence) != 2 {
		t.Fatalf("want claim evidence plus status evidence, got %d: %+v", len(f.Evidence), f.Evidence)
	}
	status := f.Evidence[1]
	if status.Source != "stellar.toml" {
		t.Errorf("status evidence source = %q, want stellar.toml", status.Source)
	}
	if status.URL != "https://circle.com/.well-known/stellar.toml" {
		t.Errorf("status evidence URL = %q, want the document's final location", status.URL)
	}
	if !strings.Contains(status.Claim, "dead") {
		t.Errorf("status claim must carry the published value verbatim: %q", status.Claim)
	}
	if status.RetrievedAt.IsZero() {
		t.Error("status evidence must carry the document's retrieval time")
	}
	if status.Attempted || status.Refused {
		t.Error("a published status is a retrieval, not an attempt or a refusal")
	}
}

// TestDomainCheckEmitsNoStatusEvidenceWhenAbsent pins the other half of the
// change: an entry that publishes no status emits no status claim, so the
// evidence bytes — and therefore evidence_hash — are unchanged for every
// asset whose entry sets no status.
func TestDomainCheckEmitsNoStatusEvidenceWhenAbsent(t *testing.T) {
	f := runDomain(t, domainSubject(t, `
[[CURRENCIES]]
code = "USDC"
issuer = "`+domainIssuer+`"
`))

	if len(f.Evidence) != 1 {
		t.Fatalf("want exactly one evidence entry, got %d: %+v", len(f.Evidence), f.Evidence)
	}
	if strings.Contains(f.Evidence[0].Claim, "status") {
		t.Errorf("no-status entry emitted a status claim: %q", f.Evidence[0].Claim)
	}
	if f.Severity != mechanics.Clear {
		t.Errorf("severity = %v, want Clear", f.Severity)
	}
	if f.Accountability == nil || *f.Accountability != mechanics.AccountabilityVerified {
		t.Errorf("accountability = %v, want verified", f.Accountability)
	}
}

// TestDomainCheckIgnoresStatusOfOtherEntries: a status belongs to the entry
// that claims THIS asset. A status on any other entry — another asset's, or a
// same-code entry under a different issuer — is not this asset's status and
// must not be surfaced for it.
func TestDomainCheckIgnoresStatusOfOtherEntries(t *testing.T) {
	f := runDomain(t, domainSubject(t, `
[[CURRENCIES]]
code = "USDC"
issuer = "`+domainIssuer+`"

[[CURRENCIES]]
code = "USDC"
issuer = "`+otherIssuer+`"
status = "dead"
`))

	for _, e := range f.Evidence {
		if strings.Contains(e.Claim, "status") {
			t.Errorf("another issuer's status was surfaced for this asset: %q", e.Claim)
		}
	}
	if len(f.Evidence) != 1 {
		t.Errorf("want exactly one evidence entry, got %d: %+v", len(f.Evidence), f.Evidence)
	}
}

// TestDomainCheckSurfacesOrganisationAttribution is the attribution change: a
// stellar.toml may declare who publishes it — ORG_NAME and ORG_URL — and a
// human reading the report wants to know who claims to be behind the asset.
// The claim is the toml's, not Assay's, so it is emitted as attributed evidence
// carrying the source and the URL the fields were read from — and moves
// nothing else: severity stays Clear (attribution is never capability, and
// buying a domain must not buy a severity) and accountability stays Verified,
// which the reciprocity test above already settled whatever the document calls
// itself.
func TestDomainCheckSurfacesOrganisationAttribution(t *testing.T) {
	f := runDomain(t, domainSubject(t, `
[DOCUMENTATION]
ORG_NAME = "Evil Corp"
ORG_URL = "https://evil.example/"

[[CURRENCIES]]
code = "USDC"
issuer = "`+domainIssuer+`"
`))

	if f.Severity != mechanics.Clear {
		t.Errorf("severity = %v, want Clear: attribution must never move severity", f.Severity)
	}
	if f.Accountability == nil || *f.Accountability != mechanics.AccountabilityVerified {
		t.Errorf("accountability = %v, want verified: reciprocity settled it, not the org fields", f.Accountability)
	}
	if f.Mechanics&mechanics.MechDomainUnverified != 0 {
		t.Error("an attributed claim is still a verified claim; the unverified bit must stay unset")
	}

	if len(f.Evidence) != 2 {
		t.Fatalf("want claim evidence plus organisation evidence, got %d: %+v", len(f.Evidence), f.Evidence)
	}
	org := f.Evidence[1]
	if org.Source != "stellar.toml" {
		t.Errorf("organisation evidence source = %q, want stellar.toml", org.Source)
	}
	if org.URL != "https://circle.com/.well-known/stellar.toml" {
		t.Errorf("organisation evidence URL = %q, want the document's final location", org.URL)
	}
	// Both fields, quoted verbatim — the reader must be able to copy the claim
	// into a search box, and an unquoted value could visually absorb the next
	// field into itself.
	if !strings.Contains(org.Claim, `ORG_NAME "Evil Corp"`) {
		t.Errorf("organisation claim does not carry ORG_NAME verbatim: %q", org.Claim)
	}
	if !strings.Contains(org.Claim, `ORG_URL "https://evil.example/"`) {
		t.Errorf("organisation claim does not carry ORG_URL verbatim: %q", org.Claim)
	}
	if org.RetrievedAt.IsZero() {
		t.Error("organisation evidence must carry the document's retrieval time")
	}
	if org.Attempted || org.Refused {
		t.Error("a declared organisation is a retrieval, not an attempt or a refusal")
	}
}

// TestDomainCheckEmitsNoOrganisationEvidenceWhenAbsent pins the other half of
// the change: a document that declares no organisation emits no organisation
// claim, so the evidence bytes — and therefore evidence_hash — are unchanged
// for every asset whose toml publishes neither field. Also pins that a lone
// field still emits exactly one claim rather than one padded claim with an
// empty other half.
func TestDomainCheckEmitsNoOrganisationEvidenceWhenAbsent(t *testing.T) {
	plain := runDomain(t, domainSubject(t, `
[[CURRENCIES]]
code = "USDC"
issuer = "`+domainIssuer+`"
`))
	if len(plain.Evidence) != 1 {
		t.Fatalf("want exactly one evidence entry, got %d: %+v", len(plain.Evidence), plain.Evidence)
	}
	if strings.Contains(plain.Evidence[0].Claim, "ORG_") {
		t.Errorf("a document declaring no organisation emitted an organisation claim: %q", plain.Evidence[0].Claim)
	}

	onlyURL := runDomain(t, domainSubject(t, `
[DOCUMENTATION]
ORG_URL = "https://evil.example/"

[[CURRENCIES]]
code = "USDC"
issuer = "`+domainIssuer+`"
`))
	if len(onlyURL.Evidence) != 2 {
		t.Fatalf("want claim evidence plus organisation evidence, got %d: %+v", len(onlyURL.Evidence), onlyURL.Evidence)
	}
	claim := onlyURL.Evidence[1].Claim
	if !strings.Contains(claim, `ORG_URL "https://evil.example/"`) {
		t.Errorf("claim does not carry ORG_URL: %q", claim)
	}
	// The unpublished half must be absent, not rendered as an empty field: an
	// `ORG_NAME ""` would be a claim the document never made.
	if strings.Contains(claim, "ORG_NAME") {
		t.Errorf("claim carries an ORG_NAME the document never published: %q", claim)
	}
}

// TestDomainCheckWithholdsOrganisationEvidenceWithoutAClaim: the organisation
// fields are attributed alongside a document that claims THIS asset. A toml
// that never claimed the asset publishes no claim to attribute — attaching its
// org fields to the report would read as “who is behind this asset” for an
// association the very same finding says was never reciprocated. The fields are
// parsed either way (see sep1.Doc); they surface only where the claim does.
func TestDomainCheckWithholdsOrganisationEvidenceWithoutAClaim(t *testing.T) {
	f := runDomain(t, domainSubject(t, `
[DOCUMENTATION]
ORG_NAME = "Nobody Ltd"
ORG_URL = "https://nobody.example/"

[[CURRENCIES]]
code = "EURC"
issuer = "`+otherIssuer+`"
`))

	if f.Accountability == nil || *f.Accountability != mechanics.AccountabilityUnverified {
		t.Fatalf("accountability = %v, want unverified", f.Accountability)
	}
	if len(f.Evidence) != 1 {
		t.Fatalf("want exactly one evidence entry, got %d: %+v", len(f.Evidence), f.Evidence)
	}
	if strings.Contains(f.Evidence[0].Claim, "ORG_") {
		t.Errorf("organisation of a non-claiming document was attributed to the asset: %q", f.Evidence[0].Claim)
	}
}

// networkPassphraseClaim is the claim shape appendNetworkPassphraseEvidence
// must contain: both sides of the contradiction, quoted, so a reader of the
// report can see which document declared which network without leaving it.
const networkPassphraseClaim = `NETWORK_PASSPHRASE "Test SDF Network ; September 2015"`

// TestDomainCheckReportsNetworkPassphraseMismatch: a toml that declares
// NETWORK_PASSPHRASE for another network is contradicted by the ledger the
// scan read from, and the contradiction is reported as attributed evidence —
// source, URL and retrieval time, both sides quoted (#370).
//
// It moves nothing else. Severity stays capability-only and accountability
// stays what the reciprocity test settled: the network a document thinks it is
// published on is not a statement about what the issuer can do to a balance,
// and a wrong answer there neither verifies nor refutes the asset claim.
func TestDomainCheckReportsNetworkPassphraseMismatch(t *testing.T) {
	const claiming = `
NETWORK_PASSPHRASE = "Test SDF Network ; September 2015"

[[CURRENCIES]]
code = "USDC"
issuer = "` + domainIssuer + `"
`
	// The document must be reported whatever else it says: a toml that never
	// named this asset, and a scan whose curated directory already disagrees
	// with the issuer, both return from the check through different paths, and
	// neither may drop the contradiction on the way out.
	t.Run("claiming document", func(t *testing.T) {
		s := domainSubject(t, claiming)
		s.Network = horizon.PublicNet
		f := runDomain(t, s)

		if f.Severity != mechanics.Clear {
			t.Errorf("severity = %v, want Clear: a passphrase claim must never move severity", f.Severity)
		}
		if f.Accountability == nil || *f.Accountability != mechanics.AccountabilityVerified {
			t.Errorf("accountability = %v, want verified: reciprocity settled it, not the passphrase", f.Accountability)
		}
		if f.Mechanics&mechanics.MechDomainUnverified != 0 {
			t.Error("a network contradiction is not a failed domain verification; the unverified bit must stay unset")
		}
		e := findNetworkEvidence(t, f)
		if e.Source != "stellar.toml" {
			t.Errorf("evidence source = %q, want stellar.toml", e.Source)
		}
		if e.URL != "https://circle.com/.well-known/stellar.toml" {
			t.Errorf("evidence URL = %q, want the document's final location", e.URL)
		}
		if e.RetrievedAt.IsZero() {
			t.Error("evidence must carry the document's retrieval time")
		}
		if e.Attempted || e.Refused {
			t.Error("a declared passphrase is a retrieval, not an attempt or a refusal")
		}
		for _, want := range []string{networkPassphraseClaim, `"Public Global Stellar Network ; September 2015"`} {
			if !strings.Contains(e.Claim, want) {
				t.Errorf("claim = %q, want it to quote %q", e.Claim, want)
			}
		}
	})

	t.Run("document that does not claim the asset", func(t *testing.T) {
		s := domainSubject(t, `
NETWORK_PASSPHRASE = "Test SDF Network ; September 2015"

[[CURRENCIES]]
code = "EURC"
issuer = "`+otherIssuer+`"
`)
		s.Network = horizon.PublicNet
		f := runDomain(t, s)
		findNetworkEvidence(t, f)
	})

	t.Run("directory disagreement returned first", func(t *testing.T) {
		s := domainSubject(t, claiming)
		s.Network = horizon.PublicNet
		s.Directory = &stellarexpert.DirectoryEntry{Domain: "elsewhere.example"}
		s.DirectoryURL = "https://api.stellar.expert/explorer/directory/" + domainIssuer
		f := runDomain(t, s)
		if f.Accountability == nil || *f.Accountability != mechanics.AccountabilityUnverified {
			t.Fatalf("accountability = %v, want unverified: the directory disagrees", f.Accountability)
		}
		findNetworkEvidence(t, f)
	})
}

// TestDomainCheckWithholdsNetworkEvidenceWithoutAContradiction pins the other
// half of #370: a matching declaration and an absent declaration both produce
// no evidence, so the evidence bytes — and therefore evidence_hash — are
// unchanged for every asset attested without a contradiction. A scan that
// never declared a network has nothing to compare against and is reported the
// same way as absence.
func TestDomainCheckWithholdsNetworkEvidenceWithoutAContradiction(t *testing.T) {
	const claiming = `
[[CURRENCIES]]
code = "USDC"
issuer = "` + domainIssuer + `"
`
	cases := []struct {
		name    string
		toml    string
		network horizon.Network
	}{
		{
			name:    "matching passphrase",
			toml:    "NETWORK_PASSPHRASE = \"Public Global Stellar Network ; September 2015\"\n" + claiming,
			network: horizon.PublicNet,
		},
		{
			name:    "absent passphrase",
			toml:    claiming,
			network: horizon.PublicNet,
		},
		{
			name:    "scan declared no network",
			toml:    "NETWORK_PASSPHRASE = \"Test SDF Network ; September 2015\"\n" + claiming,
			network: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := domainSubject(t, tc.toml)
			s.Network = tc.network
			f := runDomain(t, s)
			for _, e := range f.Evidence {
				if strings.Contains(e.Claim, "NETWORK_PASSPHRASE") {
					t.Errorf("NETWORK_PASSPHRASE evidence without a contradiction: %q", e.Claim)
				}
			}
			if len(f.Evidence) != 1 {
				t.Errorf("want exactly the claim's evidence entry, got %d: %+v", len(f.Evidence), f.Evidence)
			}
		})
	}
}

// findNetworkEvidence returns the NETWORK_PASSPHRASE entry, failing the test
// when the check did not emit one.
func findNetworkEvidence(t *testing.T, f mechanics.Finding) mechanics.Evidence {
	t.Helper()
	for _, e := range f.Evidence {
		if strings.Contains(e.Claim, "NETWORK_PASSPHRASE") {
			return e
		}
	}
	t.Fatalf("no NETWORK_PASSPHRASE evidence, got %d entries: %+v", len(f.Evidence), f.Evidence)
	return mechanics.Evidence{}
}

// TestDomainCheckFetchFailedIsFetchFailed pins that a stellar.toml that could
// not be read (HTTP 404 here; DNS failure and host-policy refusal take the
// same path) reports accountability unverified with reason fetch_failed, so a
// consumer switching on JSON can tell a broken web server from a refused claim.
func TestDomainCheckFetchFailedIsFetchFailed(t *testing.T) {
	s := &mechanics.Subject{
		Asset:           mechanics.Asset{Code: "USDC", Issuer: domainIssuer},
		Issuer:          &horizon.Account{AccountID: domainIssuer, HomeDomain: "circle.com"},
		Toml:            nil,
		TomlURL:         sep1.URLFor("circle.com"),
		TomlAttemptedAt: time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC),
		TomlErr:         "status 404",
		Directory:       nil,
		ScannedAt:       time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC),
	}
	f := runDomain(t, s)

	if f.Accountability == nil || *f.Accountability != mechanics.AccountabilityUnverified {
		t.Fatalf("accountability = %v, want unverified", f.Accountability)
	}
	if f.AccountabilityReason == nil || *f.AccountabilityReason != mechanics.AccountabilityReasonFetchFailed {
		t.Fatalf("accountability_reason = %v, want fetch_failed", f.AccountabilityReason)
	}
	if f.Mechanics&mechanics.MechDomainUnverified == 0 {
		t.Error("the unverified bit must be set")
	}
	// Negative: a fetch failure is not a "not claimed" verdict.
	if strings.Contains(f.Reasoning, "has not claimed this asset") {
		t.Error("fetch failure reported the flat negative 'has not claimed this asset'")
	}
	if !strings.Contains(f.Reasoning, "could not be read") {
		t.Error("fetch failure reasoning must say the toml could not be read")
	}
}

// TestDomainCheckNotClaimedIsNotClaimed pins that a stellar.toml that resolved
// but does not list this asset reports accountability unverified with reason
// not_claimed — distinguishable from a fetch failure from JSON alone.
func TestDomainCheckNotClaimedIsNotClaimed(t *testing.T) {
	toml := "[[CURRENCIES]]\ncode = \"EURC\"\nissuer = \"" + otherIssuer + "\"\n"
	f := runDomain(t, domainSubject(t, toml))

	if f.Accountability == nil || *f.Accountability != mechanics.AccountabilityUnverified {
		t.Fatalf("accountability = %v, want unverified", f.Accountability)
	}
	if f.AccountabilityReason == nil || *f.AccountabilityReason != mechanics.AccountabilityReasonNotClaimed {
		t.Fatalf("accountability_reason = %v, want not_claimed", f.AccountabilityReason)
	}
	// Negative: a fetched-but-not-claimed toml is not a fetch failure.
	if strings.Contains(f.Reasoning, "could not be read") {
		t.Error("not-claimed reasoning described the toml as unreadable")
	}
	if !strings.Contains(f.Reasoning, "has not claimed this asset") {
		t.Error("not-claimed reasoning must state the domain did not claim the asset")
	}
}

// TestDomainCheckMismatchIsMismatch pins that a home_domain that disagrees
// with the curated directory's domain reports accountability unverified with
// reason mismatch.
func TestDomainCheckMismatchIsMismatch(t *testing.T) {
	toml := "[[CURRENCIES]]\ncode = \"USDC\"\nissuer = \"" + domainIssuer + "\"\n"
	s := domainSubject(t, toml)
	s.Directory = &stellarexpert.DirectoryEntry{
		Address: otherIssuer,
		Name:    "Someone Else",
		Domain:  "other.example",
	}

	f := runDomain(t, s)

	if f.Accountability == nil || *f.Accountability != mechanics.AccountabilityUnverified {
		t.Fatalf("accountability = %v, want unverified", f.Accountability)
	}
	if f.AccountabilityReason == nil || *f.AccountabilityReason != mechanics.AccountabilityReasonMismatch {
		t.Fatalf("accountability_reason = %v, want mismatch", f.AccountabilityReason)
	}
	if !strings.Contains(f.Reasoning, "disagree") {
		t.Error("mismatch reasoning must state the two sources disagree")
	}
}

// TestDomainCheckNoHomeDomainIsUnknownNoHomeDomain pins that an issuer with no
// home_domain reports accountability unknown with reason no_home_domain, and
// never unverified: there was no claim to fail.
func TestDomainCheckNoHomeDomainIsUnknownNoHomeDomain(t *testing.T) {
	s := &mechanics.Subject{
		Asset:     mechanics.Asset{Code: "USDC", Issuer: domainIssuer},
		Issuer:    &horizon.Account{AccountID: domainIssuer}, // no home_domain
		Toml:      nil,
		ScannedAt: time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC),
	}
	f := runDomain(t, s)

	if f.Accountability == nil || *f.Accountability != mechanics.AccountabilityUnknown {
		t.Fatalf("accountability = %v, want unknown", f.Accountability)
	}
	if f.AccountabilityReason == nil || *f.AccountabilityReason != mechanics.AccountabilityReasonNoHomeDomain {
		t.Fatalf("accountability_reason = %v, want no_home_domain", f.AccountabilityReason)
	}
	// Negative: no claim is not a failed claim.
	if strings.Contains(f.Reasoning, "could not be read") {
		t.Error("no-home_domain reasoning described the toml as unreadable")
	}
	if strings.Contains(f.Reasoning, "has not claimed this asset") {
		t.Error("no-home_domain reasoning reported the flat negative 'has not claimed this asset'")
	}
}

// TestDomainCheckVerifiedHasNoReason pins that a reciprocal claim reports
// accountability verified with no sub-reason: a verified claim needs no
// further classification.
func TestDomainCheckVerifiedHasNoReason(t *testing.T) {
	toml := "[[CURRENCIES]]\ncode = \"USDC\"\nissuer = \"" + domainIssuer + "\"\n"
	f := runDomain(t, domainSubject(t, toml))

	if f.Accountability == nil || *f.Accountability != mechanics.AccountabilityVerified {
		t.Fatalf("accountability = %v, want verified", f.Accountability)
	}
	if f.AccountabilityReason != nil {
		t.Errorf("accountability_reason = %v, want nil for a verified claim", f.AccountabilityReason)
	}
}
