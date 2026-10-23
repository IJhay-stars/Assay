# Correcting a wrong attestation

An attestation is a claim consumers may use to admit an asset. If its claim is
wrong, treat it as an incident: stop relying on the affected result, correct the
current registry state when a verified replacement is available, and preserve
the record of both the original claim and its correction. Never quietly replace
the only public account of what happened.

This runbook covers scanner or evidence errors, incorrect attester submissions,
and later findings that an attestation was unsupported. A change in issuer
capabilities after a correct point-in-time scan is not itself an incorrect
attestation; follow [freshness guidance](freshness.md) and
[capability transitions](transitions.md) for that case.

## Detection and decision

A discrepancy may be raised by any scanner maintainer, attester, verifier, or
consumer. The reporter should identify the asset and network, the registry
address, the attestation transaction (if known), what appears wrong, and the
evidence or reproduction that supports the concern. A verifier can compare the
stored values and `evidence_hash` with a fresh scan using
[`assay attestation`](attestation-run.md), but a matching hash only shows that
the scanner produced those bytes. It does not establish that the scanner
interpreted its sources correctly.

The maintainers responsible for the scanner and attestation decide whether the
claim is valid, invalid, or still unknown. The admin/attester executes the
on-chain correction; it does not decide correctness by virtue of holding the
key. Base the decision on the source data and its provenance, the scanner
version and code path, a reproducible scan, and an independent review of the
interpretation. Add a regression test when the failure is in scanner logic.
Seek a second maintainer's review for consequential or disputed cases; record
the evidence and rationale either way.

- **Valid:** the original claim is supported. Make no correction. Record the
  finding and why it was closed if it was reported publicly.
- **Invalid:** the claim is demonstrably wrong or unsupported. Follow the
  corrective procedure below.
- **Unknown:** evidence is incomplete or reviewers disagree. Do not describe
  the claim as verified or silently choose a replacement. Notify consumers
  promptly that correctness is disputed and advise them to fail closed for the
  affected asset until a decision and verified replacement are available. The
  maintainers make and document the final determination from the evidence
  above; if the evidence cannot resolve it, leave the incident marked unknown.

## Corrective action

On discovery, stop submissions from the affected scanner version or check path
until the cause is understood. Identify every asset and attestation that could
have been affected; do not assume the first reported asset is the only one.

Today, the deployed registry has no usable `revoke` entrypoint. The contract's
`attest` call replaces the stored values for an asset, so the immediate on-chain
correction is to submit a new attestation derived from a fixed scanner and a
reviewed, complete live scan:

1. Fix the scanner or evidence-handling defect and reproduce the corrected
   result. Do not hand-edit severity, flags, or the evidence hash.
2. Record the original attestation's severity, flags, evidence hash,
   `attested_at`, network, registry address, and transaction ID before
   overwriting it. Also capture the affected scanner version and assets.
3. Review the corrected scan and submit it through the normal `make attest`
   path. Read it back with `make read` and record the correction transaction
   and resulting values.
4. Repeat for every affected asset and verify that no affected attestation was
   missed.

If a trustworthy replacement is not yet available, do not invent one to clear
the old claim. The current registry cannot withdraw that claim: alert
consumers immediately, recommend they block the affected asset or disable the
dependent gate, and state plainly that the on-chain value remains present until
a correction can be written. A consumer that reads the registry alone cannot
discover this incident.

The registry source now defines `revoke`, which withdraws the stored claim so
gates fail closed. It is not available at the deployed registry because that
contract cannot be upgraded in place. After migration to a registry with
`revoke` (see [deployment migration](deployment.md#migrating-to-a-registry-with-revoke)),
revoke an invalid attestation when no verified replacement is ready; then
attest the corrected result once it is reviewed. Revocation makes the current
read return `None`; it does not preserve a tombstone in contract storage, so
record the revoke transaction in the incident record. Do not imply revocation
has happened until the deployed registry and transaction confirm it.

## Consumers and historical record

For every confirmed invalid attestation, publish a correction notice in the
public issue or security advisory used to track the incident. Include the
affected asset(s), network and registry, original values and transaction,
what was wrong, affected scanner versions, the corrected or revoked values and
transaction, and the dates of discovery and correction. Link the original
notice to the correction; do not edit away the original claim or make the
correction look like an ordinary refresh. Keep the notice available as the
incident record.

Notify known integrators directly using their established contact channel, as
well as publishing the notice. State whether the old claim is still readable
on-chain and what consumers should do in the meantime. For an unknown
correctness finding, send the same warning before the final determination and
update it when the state changes. A chain event or overwritten value does not
by itself notify consumers.

For a potentially exploitable under-reporting error, use the private reporting
and coordinated disclosure process in [SECURITY.md](../SECURITY.md) before
publishing technical details. Notify known affected consumers promptly with
the mitigation they need; publish the correction notice once disclosure is
safe and the immediate risk is addressed.
