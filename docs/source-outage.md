# Source-outage runbook

Use this guide when a scan reports an upstream source failure. Assay treats
ledger facts and consumed third-party signals differently; do not interpret
either kind of outage as evidence about the asset itself.

## Identify the state

| State | What failed | Effect on scans | Effect on attestation |
| --- | --- | --- | --- |
| **valid** | All sources are reachable. | Scan completes with its normal findings. | A complete report can be attested, subject to the usual checks. |
| **unavailable** | A consumed source such as StellarExpert is unreachable. | Scan completes, but is marked `undetermined`; the failed check and source error are reported. | Attestation is refused until the report is complete. |
| **invalid** | A ledger source (Horizon) is unreachable. | Scan stops with an error; no report is produced. | No attestation can be made from that scan. |

For an unavailable result, look for `undetermined: true` and
`undetermined_checks` in the scan response (or the equivalent undetermined
status in CLI output), then inspect the failure evidence for the source and
error. For an invalid result, the scan itself returns an error; check whether
the failed request was to Horizon. A StellarExpert failure during the
2026-09-05 10-asset sweep was rate limiting; a DNS failure affected a scan on
2026-09-22.

In both cases, **already-written attestations are unchanged**. An outage neither
rewrites nor revokes them; consumers continue to apply their normal freshness
and policy rules.

## Respond

There is no guaranteed recovery time. A rate limit may clear when the
provider's limit window passes; a DNS or other connectivity failure lasts until
the provider or the affected network configuration recovers. Assay does not
retry these requests automatically.

- **StellarExpert unavailable:** record the timestamp and source error, check
  provider availability and the deployment's outbound connectivity, and pause
  a rate-limited bulk sweep. Keep the check enabled. Once the source is
  reachable, run a fresh scan and confirm it is no longer undetermined before
  following the [re-attestation runbook (#115)](https://github.com/use-assay/Assay/issues/115).
- **Horizon unavailable:** record the failed request and timestamp, check
  Horizon availability and the deployment's DNS/network path, and restore
  connectivity. Once Horizon responds, run a fresh scan; only then proceed
  with attestation under the [re-attestation runbook (#115)](https://github.com/use-assay/Assay/issues/115).

**Do not disable or suppress a check to get a report or attestation through.**
That is check suppression, not outage recovery: it hides missing evidence and
can make an incomplete result appear complete. Do not treat a failed scan or an
undetermined finding as a clean result, and do not manually attest from a
partial report.