# Runbook: the reproducibility job reports a mismatch

Use this when `.github/workflows/reproducibility.yml` fails, or when you run
`./scripts/reproducibility.sh` locally and it does not print `result: PASS`.
How the job works is in [CONTRIBUTING.md](../CONTRIBUTING.md#the-reproducibility-job);
this page is what to do about its result.

The rule underneath every branch: **an on-chain attestation is changed only by
a new scan whose evidence you have checked.** A mismatch never justifies
editing a hash in [deployment.md](deployment.md) to make the job green.

## 1. Classify

Read the job summary or `repro.log`. Each asset gets one line; classify every
line that is not `PASS`.

```
Not PASS
│
├─ INCONCLUSIVE ASSET :: …     (script exit 2)
│     → Outage. Not a reproducibility result.          → Action A
│
├─ ENCODING ASSET recorded vN … (script exit 3)
│     → Encoding drift: same evidence, the table names
│       the wrong preimage version (or the encoder moved). → Action B
│
└─ MISMATCH ASSET expected X actual Y   (script exit 1)
      Re-scan the asset and compare the evidence with
      `assay verify` / the evidence-only detector:
      │
      ├─ Only the wording of a recorded fetch failure moved
      │  (`failure_text`, the #24 signature; BERKSHIRE,
      │   DOGE and KALE are known to carry it)
      │     → Transport text. Nothing about the asset changed. → Action C
      │
      ├─ A source's answer changed, and the verdict
      │  (severity, flags) did not
      │     → Evidence drift.                                 → Action D
      │
      └─ The verdict changed, or the evidence does not
         explain the hash at all
            → Genuine error until proven otherwise.          → Action E
```

To compare evidence, scan the asset twice with recordings and diff them, or
compare against the bundle written at attestation time
([offline-bundle.md](offline-bundle.md)). The
[evidence-only detector](transitions.md#evidence-only-changes) tells "a source
went silent" from "a source changed its answer" from "only the failure text
moved".

If every asset fails at once (as on 2026-10-12), suspect the runner first: DNS,
an upstream outage, or an encoder change merged since the last green run.
Check `git log` for `internal/attest` and re-run once before classifying assets
one by one.

## 2. Act: one action per class

| Class | Action | Re-attest? |
| --- | --- | --- |
| **A. Inconclusive** (outage) | Re-run when sources recover: `gh workflow run reproducibility.yml`. If it persists past a day, follow [source-outage.md](source-outage.md). | No. An outage is not evidence about the asset. |
| **B. Encoding drift** | Fix the table or the encoder, not the evidence. If the `Preimage` column in [deployment.md](deployment.md) is wrong, correct it, citing the run. If the encoder changed the bytes of an existing version, that is a regression in `internal/attest`: revert it or restore the version. | No. The evidence did not change. |
| **C. Transport text (#24)** | Record the run against [#24](https://github.com/use-assay/Assay/issues/24) and close the alert. Confirm the event is `failure_text` and that no other source moved. | No. Re-attesting would only bake the new runner's wording into the hash. |
| **D. Evidence drift** | Investigate what the source now says, and record it in [attestation-run.md](attestation-run.md). Then re-attest with `make attest ASSET=…`, update the hash in [deployment.md](deployment.md), and run the job again. | Yes, after review. The attestation is stale with respect to the evidence. |
| **E. Genuine error** | Stop and investigate before anything is written. If the on-chain severity is now wrong in the unsafe direction (the asset is worse than attested), withdraw it so gates fail closed: `revoke` on a registry that has it. The live testnet registry predates `revoke` ([deployment.md](deployment.md#migrating-to-a-registry-with-revoke)), so there, re-attest the corrected severity instead. Open an issue with both hashes and both evidence sets. | Only after the cause is understood. Withdraw or correct first if the error is unsafe. |

## 3. Close out

- Note the class and the action in the tracking issue the scheduled run opened
  (`scripts/notify-scheduled-failure.sh`), then let the next green run close it.
- Classes D and E change what is on chain. The re-attestation and any `revoke`
  belong in [attestation-run.md](attestation-run.md), with the date and the
  reason.
