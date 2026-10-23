# Changelog

Notable changes to Assay's four surfaces that move independently of each
other: the evidence encoding, the contract ABI, the CLI and HTTP API, and the
deployments. Backfilled to the first deployment from `docs/sessions/`,
`docs/contract-interface.md`, `docs/deployment.md` and the merged PRs; a date
is when the change landed on `main`, not when it was written or deployed.

Two honesty notes that govern this file:

- **Source is not deployment.** The registry contract on testnet is the
  2026-08-15 build. ABI entries after that date are in the source and are not
  on chain until a redeploy is recorded under [Deployments](#deployments-stellar-testnet).
  [`docs/deployment.md`](docs/deployment.md) is the record of what is actually
  deployed; this file is the record of what changed.
- **Preimage versions are forever.** An `evidence_hash` already written is
  bound to the encoding version that produced it. New versions add tags and
  layouts; they never reinterpret bytes written under an older tag. The exact
  layouts are specified by [`docs/contract-interface.md`](docs/contract-interface.md)
  and reproducible with `assay attestation -preimage CODE-ISSUER`.

PRs touching one of these surfaces add a dated entry at the top of the
matching section — see [CONTRIBUTING.md](CONTRIBUTING.md#changelog).

## Evidence encoding (`evidence_hash` preimage)

- **2026-10-19 — `assay-evidence-v4`: one layout per tag.** The `scanner`
  line moves under its own tag, immediately after the version line. Before
  this, `assay-evidence-v3` covered three layouts (scanner-only,
  network-only, both), so a reimplementer reading the tag could not know
  which bytes to expect. Now every tag admits exactly one layout, and a v3
  preimage carrying a `scanner` line — or a v4 missing one — is refused as
  invalid rather than guessed at
  ([17bccc8](https://github.com/use-assay/Assay/commit/17bccc8)).
- **2026-10-18 — v1 bytes restored after drift.** The v1 encoding had
  silently changed under its own tag; the original bytes are restored and
  reproduced under a recorded encoding, and the scanner identity in v4 is
  derived from build info rather than a runtime guess
  ([#269](https://github.com/use-assay/Assay/issues/269),
  [#273](https://github.com/use-assay/Assay/issues/273)–[#276](https://github.com/use-assay/Assay/issues/276),
  [#278](https://github.com/use-assay/Assay/issues/278),
  [5eb402f](https://github.com/use-assay/Assay/commit/5eb402f)).
- **2026-09-27 — `assay-evidence-v3`: the network binds.** A `network` line
  after `checks` carries the ledger passphrase the facts were read from, so
  a testnet attestation's bytes can never be replayed as a pubnet one. The
  `network-bound-testnet` / `network-bound-pubnet` fixtures are
  byte-identical except for that line — the property v3 exists for
  ([#41](https://github.com/use-assay/Assay/issues/41), merged via
  [#231](https://github.com/use-assay/Assay/pull/231)).
- **2026-09-26 — `assay-evidence-v2`: the check set binds.** A `checks` line
  after `accountability` names, sorted and never empty, the checks the engine
  actually ran, so an attestation cannot later be read as evidence for a
  check that never ran. Covered by the `checkset-bound-v2` regression vector
  ([#185](https://github.com/use-assay/Assay/pull/185)).
- **2026-08-15 — `assay-evidence-v1`: the canonical encoding.**
  `internal/attest` introduces the tab-separated, LF-terminated preimage
  (`asset`, `severity`, `flags`, `accountability`, …) whose SHA-256 is the
  on-chain `evidence_hash`. A v1 preimage has no `checks` line; an encoder
  that emits one produces bytes no v1 verifier accepts. Existing attestations
  (AQUA, USDC, USDZ, BERKSHIRE) are v1 and their digests are the on-chain
  hashes.

## Contract ABI

### safety-registry

- **2026-10-20 — `INTERFACE_VERSION` 1 → 2: `cancel_admin_transfer`.** The
  admin can withdraw a pending two-step transfer; a cancellation with
  nothing pending returns `NoPendingAdmin` rather than succeeding quietly,
  and publishes an `AdminTransferCancelled` event
  ([#320](https://github.com/use-assay/Assay/issues/320), merged via
  [#501](https://github.com/use-assay/Assay/pull/501)).
- **2026-10-18 — `version()` and `get_safety_many`.** `version()` returns
  `INTERFACE_VERSION`; adding it establishes interface version 1, and
  consumers and the ABI-drift test key off it. `get_safety_many` reads up to
  a bound number of assets in one call, returning `None` per missing asset
  ([#330](https://github.com/use-assay/Assay/issues/330)–[#333](https://github.com/use-assay/Assay/issues/333),
  merged via [#482](https://github.com/use-assay/Assay/pull/482)).
- **2026-09-27 — `revoke`, write-time TTL extension, and `attest_many`.**
  `revoke` clears an attestation (reads as `None`, distinct from archived);
  writes extend the instance TTL to the network maximum; `attest_many` adds
  batched writes, all-or-nothing, capped at a measured batch size
  ([#217](https://github.com/use-assay/Assay/pull/217),
  [#10](https://github.com/use-assay/Assay/issues/10)). Same day: the masked
  policy gate `is_safe_masked`, per-asset attestation events, and the
  ABI-drift test that pins this table to the source
  ([#194](https://github.com/use-assay/Assay/pull/194)).
- **2026-09-26 — two-step admin transfer.** `transfer_admin` nominates a
  successor (admin-auth), `accept_admin` promotes it (successor-auth), so
  the admin role — the root of who may write attestations — cannot be
  transferred to an unowned address or typo in one step
  ([#82](https://github.com/use-assay/Assay/issues/82),
  [#83](https://github.com/use-assay/Assay/issues/83)). Reads learn to
  distinguish never-attested from archived
  ([#90](https://github.com/use-assay/Assay/issues/90)).
- **2026-08-11 — the initial ABI.** `init(admin)`, `attest`, `get_safety`
  and `is_safe`; the `Safety` struct is `{ severity, flags, evidence_hash,
  attested_at }` and `is_safe` fails closed on the unattested, stale, and
  over-threshold paths
  ([4908974](https://github.com/use-assay/Assay/commit/4908974)).

### example-gate

- **2026-10-20 — `policy()` and deposit validation.** The gate exposes its
  configured policy for on-chain inspection and validates the deposit path;
  the decision *not* to store the scan time on chain is recorded as a
  decision ([#334](https://github.com/use-assay/Assay/issues/334)–[#337](https://github.com/use-assay/Assay/issues/337),
  merged via [#495](https://github.com/use-assay/Assay/pull/495)).
- **2026-09-16 — severity ceiling.** The gate stops admitting
  critical-by-reputation assets, fixing the admission the first deployment
  allowed ([#26](https://github.com/use-assay/Assay/issues/26)). See
  [Deployments](#deployments-stellar-testnet) for the redeploy.

## CLI and HTTP API

- **2026-10-20 — `assay diff`, offline bundles, verify-all script.** `diff`
  explains what changed between two observations; the offline bundle archives
  the exact source bytes behind an attestation and verifies them without a
  network; `verify-all` re-checks every attested asset end to end
  ([#456](https://github.com/use-assay/Assay/issues/456),
  [#462](https://github.com/use-assay/Assay/issues/462)–[#465](https://github.com/use-assay/Assay/issues/465),
  merged via [#504](https://github.com/use-assay/Assay/pull/504),
  [#496](https://github.com/use-assay/Assay/pull/496)).
- **2026-10-17 — `assay registry-history`.** The on-chain observation history
  for one asset, read from the registry, with indexer notes
  ([83755b5](https://github.com/use-assay/Assay/commit/83755b5)).
- **2026-09-27 — `assay verify`, `/api/v1/history`, and server hardening.**
  `verify` is a standalone evidence verifier: a third party checks a
  downloaded binary and then an attestation without reading the source. The
  HTTP API gains the observation-history endpoints and the
  `X-Assay-Undetermined` response header; the server gains read, write and
  idle timeouts and graceful shutdown
  ([#177](https://github.com/use-assay/Assay/pull/177),
  [a396ee3](https://github.com/use-assay/Assay/commit/a396ee3),
  [f77c7fc](https://github.com/use-assay/Assay/commit/f77c7fc)).
- **2026-09-24 — `assay history`.** Per-asset scan history from the CLI,
  alongside Horizon record-verification fixes and rate-limit-aware user
  agent headers ([e25308d](https://github.com/use-assay/Assay/commit/e25308d)).
- **2026-08-15 — `assay attestation`.** Prints the on-chain `attest()`
  arguments derived from a live scan — no path lets a hand-written severity
  reach the contract — with `-preimage` for the exact hashed bytes and `-raw`
  for scripting.
- **2026-08-10 — the first CLI and API.** `GET /api/v1/scan?asset=CODE-ISSUER`
  with the single-file UI, and the `assay` binary's scan path
  ([7111853](https://github.com/use-assay/Assay/commit/7111853)).

## Deployments (Stellar testnet)

Nothing is deployed on pubnet, and testnet is periodically reset. Transaction
hashes and wasm hashes are in
[`docs/deployment.md`](docs/deployment.md); any of them can be read at
`https://stellar.expert/explorer/testnet/tx/<hash>`.

- **Registry — deployed 2026-08-15, unchanged since.**
  `CBK4FBIHMDTXCUPE4E3ZDVSFJSCY5FJETTKNIQPN4LFJIKKIBLKIXQ73`, wasm
  `c4105b91…dd301b`, `init(admin)` in the same session. This is the only
  registry deployment: every ABI entry above dated after 2026-08-15 is
  source-level and awaiting a redeploy. The live entries are archived on
  reset and restored on read with their original `attested_at`.
- **2026-09-16 — example gate redeployed with the severity ceiling.**
  `CAL5VYSWLKG367D5IYGI57XH7EMN5PLJ4CD6K3MO2HJBYYKEKPG3NKRX`, wasm
  `ca40172e…e8987c`, wired to the registry above
  ([#26](https://github.com/use-assay/Assay/issues/26)). The original
  2026-08-15 gate (`CANO57JR…`) is **superseded, do not use** — it admits
  critical-by-reputation assets. A second identical instance
  (`CCMA2SW23W…`) deployed six minutes after the fix is recorded as a
  mistake: unused, unreferenced, and undeletable.
- **2026-08-15 — first deployment.** Registry uploaded, deployed and
  `init`-ed; the original example gate deployed alongside it; four assets
  (AQUA, USDC, USDZ, BERKSHIRE) attested from live scans, making the
  scan → attest → `get_safety` round trip reproducible end to end.
