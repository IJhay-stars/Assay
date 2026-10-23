# `assay-attester`: dry-run and issuer watcher

`cmd/assay-attester` is the keyless half of the writer designed in
[attestation-writer.md](attestation-writer.md). It implements the parts that
need no key material:

- `--dry-run --once`: scan, derive `attest()` arguments, derive the SAC address, print, and stop before signing.
- `--watch`: report authorization-flag changes on attested issuers as they land.
- `diff`: compare two dry-run outputs (used by CI).

**There is no signing mode.** Running without `--dry-run` or `--watch` is
refused, and no code path in this command loads a key. The full writer waits
on key custody ([#150](https://github.com/use-assay/Assay/issues/150), see
[attester-key.md](attester-key.md)) and the re-attestation runbook
([#145](https://github.com/use-assay/Assay/issues/145)). Until then, writes
go through `make attest`, one asset at a time.

## Configuration

```json
{
  "network": "testnet",
  "contract_id": "CBK4FBIHMDTXCUPE4E3ZDVSFJSCY5FJETTKNIQPN4LFJIKKIBLKIXQ73",
  "cadence": "24h",
  "watch_interval": "60s",
  "assets": [
    { "asset": "AQUA-GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA" }
  ]
}
```

| Field | Meaning |
| --- | --- |
| `network` | The **registry's** network: `testnet`, `pubnet`, `futurenet`, `standalone` or a full passphrase. The SAC address is derived for this network, not the scan's ([why](attestation-writer.md#2-what-is-signed)). |
| `contract_id` | The registry contract. Validated as a `C…` address. |
| `cadence` | The re-attestation schedule, as a Go duration. Recorded, not acted on by the dry-run. |
| `watch_interval` | How often `--watch` polls each issuer. Default `60s`. |
| `horizon_url` | The Horizon `--watch` polls. Default pubnet, where the attested assets live. |
| `fixtures_dir` | Offline mode: scan from committed fixtures instead of live sources. Each asset must then name its `fixture` subject. |
| `scan_network` | Offline mode only: the network a fixture report is bound to. Default pubnet, where the fixtures were captured. |
| `assets` | The attested set, as `CODE-ISSUER`, each listed once. An empty list is refused: an empty run is an error. |

Unknown fields are an error, so a mistyped key cannot silently fall back to a
default. [`attested.json`](../attested.json) is the live attested set (the ten
assets in [deployment.md](deployment.md)). [`.github/attester/fixtures.json`](../.github/attester/fixtures.json)
is the offline set CI uses.

## Dry-run

```sh
go run ./cmd/assay-attester --config attested.json --dry-run --once
```

Each asset is scanned exactly as `assay scan` scans it. `attest.FromReport`
then derives the arguments, with the same refusals as `assay attestation`, and
the SAC address is derived for the registry's network. One line per asset,
tab-separated:

```
CODE-ISSUER  SAC  SEVERITY  FLAGS  EVIDENCE_HASH    would attest
CODE-ISSUER  SAC  refused   REASON                  attest refused the report
CODE-ISSUER  SAC  error     REASON                  the asset could not be evaluated
```

A would-attest line is `assay attestation -raw` with the asset and SAC address
in front. A refusal carries `attest`'s reason verbatim. Tabs and line breaks
in a reason are escaped (`\t`, `\n`) so each asset stays on one line. The SAC
address is printed even for a refusal: it depends only on the config.

Exit codes, as the design specifies:

| Code | Meaning |
| --- | --- |
| `0` | Every asset would be attested. |
| `1` | At least one asset was refused (undetermined, unevaluated, stale or inconsistent), and nothing failed. |
| `2` | An operational failure: config, scan transport, fixture or derivation. Wins over `1`: an asset that could not be looked at says nothing about whether it would have been refused. |

`--once` is required. A scheduled loop is not implemented; let cron or a CI
timer repeat the pass.

## Dry-run in CI

`.github/workflows/attester-dry-run.yml` runs on pull requests that touch
`internal/mechanics`, `internal/attest`, `internal/scan` or the attester
itself. It runs the dry-run over `.github/attester/fixtures.json` on the PR's
base and head, entirely offline. Then `assay-attester diff BASE HEAD` posts
the per-asset changes to severity, flags, evidence hash, status and refusal
reason as the job summary.

The job is informational. A changed attestation is often the point of a
scanner PR, so a diff never fails it; it makes the change visible in review
rather than in the nightly reproducibility job. Only an operational failure
(exit 2) of the head dry-run fails the job. The fixture set includes VELO,
whose fixture is undetermined, so the expected head exit is `1`.

## Issuer watcher

```sh
go run ./cmd/assay-attester --config attested.json --watch
```

The watcher implements the read-only half of the design's `--watch`. For
each issuer behind the attested set, it records the issuer's current flags
and its newest operation. Then every `watch_interval` it reads the issuer's
new operations, and logs a `capability transition` JSON line the moment a
`set_options` changes an authorization flag. The line carries the issuer, its
attested assets, the flags before and after, the flags added and removed, and
the operation and transaction. It never attests: it detects and reports.

Two details follow [freshness.md](freshness.md):

- **Filtering is client-side.** Horizon silently ignores `type=` on these
  endpoints (verified 2026-09-24), so the watcher fetches each issuer's full
  operation pages and keeps `set_options` itself.
- **Value changes, not records.** A `set_options` that sets a flag the issuer
  already has is a no-op, but Horizon still records it. The watcher applies
  each operation to the flags it knows and reports only actual changes. Failed
  transactions and `set_options` sourced by another account are ignored.

A failed poll is logged and retried from the same cursor on the next interval,
so a Horizon blip skips nothing.

### Cadence and Horizon load

| | Default |
| --- | --- |
| Poll interval | 60 s per issuer (`watch_interval`) |
| Requests per poll | One per issuer, plus one per extra 200-operation page of backlog |
| Attested set today | 7 distinct issuers behind 10 assets |
| Steady-state load | 7 requests/minute: about 420/hour, 10 080/day |
| Detection latency | At most one interval after the ledger closes (~5 s) |

SDF's public Horizon allows 3 600 requests per hour per IP, so the default
uses about 12% of it. Startup costs two more requests per issuer (newest
operation, current flags). An interval shorter than about 10 s gains little,
because ledgers close every ~5 s and the public rate limit is per IP. The
daily schedule in [freshness.md](freshness.md) remains the backstop; the
watcher shortens the window between a flag change and someone knowing.

## What is deliberately missing

- **Signing and submission.** No key code exists here (#145, #150).
- **Run records.** The machine-readable per-run record is specified separately
  in [attestation-writer.md](attestation-writer.md).
- **Re-attestation on a transition.** The watcher reports; acting on a report
  is the writer's job once it can sign.
