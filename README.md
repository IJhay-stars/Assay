# Assay

Mechanical trap-scanner and on-chain safety gate for Stellar assets.

Assay answers one question: **can the issuer of this asset take or freeze my
tokens after I hold them?** It answers it from the ledger's own rules — the
issuer's authorization flags — not from reputation, community reports, or a
domain blocklist.

## Live on testnet

```
Safety registry   CBK4FBIHMDTXCUPE4E3ZDVSFJSCY5FJETTKNIQPN4LFJIKKIBLKIXQ73
Network           Testnet (Test SDF Network ; September 2015)
```

`get_safety(asset)` is callable now, by any Soroban contract, atomically.
[docs/integrating.md](docs/integrating.md) has a copy-pasteable gate and a
worked example contract that is also deployed on testnet
(`CAL5VYSWLKG367D5IYGI57XH7EMN5PLJ4CD6K3MO2HJBYYKEKPG3NKRX`);
[docs/deployment.md](docs/deployment.md) has the transaction hashes, the
attested assets, and the live fail-closed checks.

## Why this exists

Stellar already has a reputation layer, and Assay does not rebuild it.
StellarExpert publishes asset ratings, a curated address directory, and a
malicious-domain blocklist; SEP-0037 defines the directory format; the SCF
runs a scam-flagging process. Assay **consumes** those as attributed input
signals and never re-derives them.

What no tool in the ecosystem does is the other half:

1. **Trap mechanics from the ledger itself.** Authorization flags are
   consensus-enforced facts about what the issuer is *able* to do. They are
   true whether or not anyone has reported the asset yet, which means a
   brand-new asset with no reputation at all is still fully classifiable.
2. **An on-chain gate.** `get_safety(asset)` is designed to be callable by
   another Soroban contract, atomically, in the same transaction as the
   action it protects.

## The hard part

Detecting a flag is trivial. Judging it is not.

`auth_clawback_enabled` is not inherently malicious — it is the mechanism
regulated issuers use to meet legal obligations like sanctions enforcement
and court-ordered reversal. A scanner that paints every clawback-capable
asset red is wrong about every well-run regulated stablecoin, and a scanner
that is wrong about the biggest assets on the network gets ignored.

So Assay's engine reports **severity plus reasoning**, never a boolean, and
carries an explicit, evaluated position on legitimate clawback use. See
[docs/severity-model.md](docs/severity-model.md) for the model and
[docs/eval.md](docs/eval.md) for the labelled set it is measured against.

## Docs

Every document under `docs/` appears in exactly one table below, grouped by
who reads it. CI fails if a document is added without a row here, or if a row
links a file that does not exist (see `internal/docindex`).

### For integrators

Call `get_safety`, run the binary, consume the API.

| Document | What's in it |
| --- | --- |
| [docs/integrating.md](docs/integrating.md) | How your contract calls `get_safety` and gates on both severity and the bitset |
| [docs/contract-interface.md](docs/contract-interface.md) | `get_safety(asset)` design and the `evidence_hash` encoding |
| [docs/checks.md](docs/checks.md) | Each mechanic Assay checks, and what it can and cannot conclude |
| [docs/cli.md](docs/cli.md) | The `assay` binary: scan, attestation, verify, diff, history, serve |
| [docs/api.md](docs/api.md) | Every HTTP endpoint, and the JSON each one returns |
| [docs/api-versioning.md](docs/api-versioning.md) | What `/api/v1` guarantees: additive vs breaking changes and the deprecation process |
| [docs/caching.md](docs/caching.md) | How the scanner caches the signals it consumes (StellarExpert directory and blocklist), and for how long |
| [docs/asset-lists.md](docs/asset-lists.md) | Consuming SEP-0042 Stellar Asset Lists as a second curation source, and why listing is never a safety signal |
| [docs/timestamps.md](docs/timestamps.md) | Observation timestamp semantics: which clock each stored time comes from, and what each one means |
| [docs/freshness.md](docs/freshness.md) | How old an attestation may be: measured flag-change rates and window guidance per use class |
| [docs/glossary.md](docs/glossary.md) | Every state Assay reports — severity, accountability, verdict state, `undetermined` — and the state each is most often confused with |
| [docs/consumer-integration-research.md](docs/consumer-integration-research.md) | Which `get_safety` consumer has the strongest evidence for a reference integration, and why |

### For verifiers

Check an attestation, investigate a verdict, audit what Assay claims.

| Document | What's in it |
| --- | --- |
| [docs/verifying.md](docs/verifying.md) | How a third party verifies a downloaded binary, then an attestation, end to end, without reading the source |
| [docs/trust.md](docs/trust.md) | What you are trusting: verifiable facts vs. relayed claims, and consequence matrix |
| [docs/severity-model.md](docs/severity-model.md) | The judgment layer: severity levels, the legitimate-use carve-out, and why |
| [docs/severity-accountability.md](docs/severity-accountability.md) | Why severity and accountability are never blended into one score, and how every surface must present both |
| [docs/temporal.md](docs/temporal.md) | The temporal trust model: what an attestation says about one instant, and why freshness is the caller's policy |
| [docs/transitions.md](docs/transitions.md) | What changed between two observations: capability, severity, accountability and evidence movement |
| [docs/history.md](docs/history.md) | The observation history API: what is retained, for how long, and what an empty history means |
| [docs/differential.md](docs/differential.md) | Differential derivation: an independent re-derivation of ledger facts, compared against what the scanner produced |
| [docs/debugging-a-verdict.md](docs/debugging-a-verdict.md) | A systematic procedure for investigating a surprising verdict back to its source |
| [docs/fail-closed.md](docs/fail-closed.md) | Every place a failure could influence the answer, classified as fail-closed or not, with the evidence |
| [docs/ever-escalated-marker.md](docs/ever-escalated-marker.md) | Design (not implemented): an on-chain record that closes the reputation/attribution threat class |
| [docs/threat-model.md](docs/threat-model.md) | The actors, what each can do, and which attacks Assay defends against, accepts, or scopes out |
| [docs/offline-bundle.md](docs/offline-bundle.md) | Archiving the exact source bytes behind an attestation, and verifying them without a network |
| [docs/eval.md](docs/eval.md) | Labelled trap/legitimate set and current results |
| [docs/severity-rating-comparison.md](docs/severity-rating-comparison.md) | Dated comparison of Assay severity with StellarExpert's rating for the top-50 fixture and scanned sample |
| [docs/wallet-clawback-survey.md](docs/wallet-clawback-survey.md) | What Freighter, LOBSTR, xBull, StellarX and Albedo warn a user about before adding a clawback-enabled trustline (surveyed 2026-10-20) |

### For contributors

Read or change the code, or review a design.

| Document | What's in it |
| --- | --- |
| [docs/first-15-minutes.md](docs/first-15-minutes.md) | **Start here as a contributor:** clone, test, scan one asset, read its report against the glossary, run the eval, find a starter issue |
| [docs/architecture.md](docs/architecture.md) | The Go scanner and the two Soroban contracts, and how the pieces fit together |
| [docs/adding-a-check.md](docs/adding-a-check.md) | How to write a new mechanic check |
| [docs/pipeline.md](docs/pipeline.md) | The full ledger-fact to on-chain-verdict path for a single asset |
| [docs/attestation-run.md](docs/attestation-run.md) | Every asset scanned, what each check returned, and what the run exposed about the scanner |
| [docs/attestation-writer.md](docs/attestation-writer.md) | Design (not implemented): the attestation writer pipeline, answering the open questions in #8 |
| [docs/multi-attestor.md](docs/multi-attestor.md) | Design (for review): approaches to multi-attestor writes, answering #88 |
| [docs/testing.md](docs/testing.md) | Package-level testing guide: which test belongs where, and how CI runs them |
| [docs/mutation-catalogue.md](docs/mutation-catalogue.md) | The mutation catalogue and baseline: which mutations were applied by hand, and what the tests caught |
| [docs/sep-asset-safety-attestation.md](docs/sep-asset-safety-attestation.md) | Draft SEP for the asset safety attestation interface |
| [docs/sessions/2026-08-10-mvp-core.md](docs/sessions/2026-08-10-mvp-core.md) | Session log: empty repo to a working scanner, eval and contract |
| [docs/sessions/2026-08-15-testnet-deployment.md](docs/sessions/2026-08-15-testnet-deployment.md) | Session log: the contract from passing local tests to live on testnet |
| [docs/sessions/2026-09-17-audit-and-provenance.md](docs/sessions/2026-09-17-audit-and-provenance.md) | Session log: an integrity audit of what is merged and how to reproduce it |

### For operators

Run the scanner and the registry.

| Document | What's in it |
| --- | --- |
| [docs/deployment.md](docs/deployment.md) | Deployed addresses, attested assets, transaction hashes |
| [docs/attester-key.md](docs/attester-key.md) | Attester key custody, loss/compromise response, rotation limits |
| [docs/attester.md](docs/attester.md) | `assay-attester`: the keyless dry-run and the issuer watcher that run ahead of the attestation writer |
| [docs/attestation-correction.md](docs/attestation-correction.md) | Runbook: treating a wrong attestation as an incident and correcting it |
| [docs/source-outage.md](docs/source-outage.md) | Runbook: what to do when a scan reports an upstream source failure |
| [docs/reproducibility-mismatch.md](docs/reproducibility-mismatch.md) | Runbook: the reproducibility job reports a mismatch |

### Translations

| Document | What's in it |
| --- | --- |
| [docs/i18n/](docs/i18n/README.md) | French and Spanish translations of the status section, glossary and integration guide; English is authoritative |

## Status

The mechanics engine, the HTTP API, and the on-chain gate all work. The gate is
deployed to testnet, 18 real assets have been scanned and 10 attested from live
scans, and the round trip — scan, attest, `get_safety` — is reproducible end to
end: re-checked on 2026-09-17, a fresh scan reproduced the stored `evidence_hash`
for every attested asset, with the limits recorded in
[#24](https://github.com/use-assay/Assay/issues/24).
[docs/verifying.md](docs/verifying.md) is the procedure a third party follows to
check one of those attestations from scratch, including what a mismatch does and
does not prove. It begins by verifying the scanner itself against its published
checksum and build provenance, because a verifier comparing evidence hashes is
otherwise trusting the binary they downloaded.

Four things are worth knowing before you rely on any of it:

- **Coverage is 10 attested assets.** Everything else on the network returns `None`.
  That is the correct answer — an asset nobody has scanned is unknown, not safe
  — but it means the registry is not yet useful as a general lookup, and a
  correctly written gate will refuse nearly everything.
- **An attestation is only as fresh as its `attested_at`.** Nothing is
  refreshing them on a schedule. Issuer flags can change after an attestation is
  written, so pass a `max_age_secs` you would actually accept rather than
  assuming someone is keeping the registry current.
  [docs/freshness.md](docs/freshness.md) measures how often flags actually
  change and recommends windows per use class.
- **This is testnet, not mainnet.** One key can write any attestation, and
  testnet is periodically reset. The live registry's entries are archived: they
  are restored on read with their original `attested_at`, and they do not read
  as missing ([docs/deployment.md](docs/deployment.md#entry-lifetime)). Nothing
  here is ready for money.
- **Severity is capability, not prediction.** It says what an issuer *can* do,
  never what they are likely to do. A regulated stablecoin with clawback and a
  scam with clawback score the same, on purpose — see
  [the severity model](docs/severity-model.md).

The gaps are not left implicit. [docs/threat-model.md](docs/threat-model.md)
names the actors, states which attacks are defended (with the test that holds
them), which are accepted risks, and which are out of scope — including the one
that matters most, that a single compromised attester key can currently cause an
admission.

## License

Apache-2.0. See [LICENSE](LICENSE).
