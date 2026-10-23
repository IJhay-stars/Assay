# Your first 15 minutes

A guided first loop through Assay: build it, test it, scan a real asset, read
what the scan says, run the evaluation, and pick something to work on.
[CONTRIBUTING.md](../CONTRIBUTING.md) is the full reference; this page is the
shortest path to having run everything once.

Tested from a fresh clone on 2026-10-22 (Linux, Go 1.27.3). Times are what
that run took; yours will differ.

## 0. What you need

- **Go 1.26.10 or newer.** `go.mod` is the source of truth. Check with
  `go version`; with a recent Go, `GOTOOLCHAIN=auto` (the default) fetches the
  right version for you.
- `git` and `make`.
- A network connection for step 3 only. Everything else runs offline.

You do **not** need Rust, the `stellar` CLI or any key. Those are for the
contracts and for writing attestations, and come later.

## 1. Clone (depends on your connection)

```sh
git clone https://github.com/use-assay/Assay.git
cd Assay
```

## 2. Run the tests (about 2 minutes)

```sh
make test
```

This is `go test -race -shuffle=on ./...`, CI's test step without the coverage
profile. It is offline: every upstream source is a stub or a committed fixture. Expect a
column of `ok` lines and no `FAIL`.

## 3. Scan one asset (about 10 seconds)

```sh
go run ./cmd/assay scan AQUA-GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA
```

This reads the live pubnet ledger (Horizon), the issuer's `stellar.toml`, and
StellarExpert, and prints the report as JSON. If a source is slow you will see
`context deadline exceeded`; run it again.

## 4. Read the report against the glossary (about 5 minutes)

Every word in the report has one meaning, defined in
[glossary.md](glossary.md). Find these fields in your output:

| Field | What AQUA returned | What it means |
| --- | --- | --- |
| `severity` | `clear` | What the issuer **can do** to a holder's balance. `clear` means it holds no special power. It does **not** mean "safe": read [SAFE](glossary.md#safe) before you ever write that word. |
| `base_severity` | `clear` | The capability-only level, before any reputation escalation. It differs from `severity` only when a curated source flagged the issuer. |
| `mechanics` | `[]` | Which powers, by ledger flag name (`auth_clawback_enabled`, `auth_revocable`, …). Empty means none. |
| `accountability` | `verified` | Who stands behind the power: the issuer's domain publishes a `stellar.toml` that claims this asset back ([accountability](glossary.md#accountability--who-stands-behind-the-power)). |
| `state` | `valid` | The verdict is complete. `unknown` would mean a check could not conclude ([verdict state](glossary.md#verdict-state--is-the-answer-complete)). |
| `undetermined` | `false` | Every source answered. When `true`, the severity is a floor, not an answer ([UNDETERMINED](glossary.md#undetermined)). |
| `findings[].check` | `capability`, `mutability`, `sep1-domain`, `reputation` | One finding per check, each with its `reasoning` and the `evidence` it rests on. |

Then compare with an asset whose issuer **does** hold a power:

```sh
go run ./cmd/assay scan USDC-GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN
```

On 2026-10-22 `USDC` reported `medium`, with `mechanics`
`["auth_revocable", "domain_unverified"]` and `accountability: unverified`. The
issuer can freeze holders' balances, which is a capability, not an accusation.
Separately, the scan could not verify that the issuer's domain claims the asset
back. Those are two different axes, and the report keeps them apart.
[checks.md](checks.md) explains each check, and
[severity-model.md](severity-model.md) explains why the levels are ordered the
way they are.

## 5. Run the evaluation (a few seconds)

```sh
make eval
```

This replays the labelled corpus (`internal/mechanics/testdata`, offline) through
the checks and prints how often each check agreed with the labels, including
`undetermined` as its own outcome. Note the warning it prints: the corpus is
small, so the numbers are illustrative. [eval.md](eval.md) describes the set.

## 6. Find a starter issue (a couple of minutes)

```sh
gh issue list -R use-assay/Assay --state open --label "good first issue"
```

Or browse [good first issues](https://github.com/use-assay/Assay/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22).
Before you start, read the two [ground rules](../CONTRIBUTING.md#ground-rules),
because every review applies them. If what you have is a report rather than a
fix, use the matching [issue template](../CONTRIBUTING.md#which-issue-template-to-use).
Anything that makes Assay **under**-report risk goes privately through
[SECURITY.md](../SECURITY.md), not into an issue.

## Where to go next

- [debugging-a-verdict.md](debugging-a-verdict.md): when a scan says something you did not expect.
- [adding-a-check.md](adding-a-check.md): how a new mechanic check is written and labelled.
- [architecture.md](architecture.md): which package does what, and where I/O is allowed.
- [CONTRIBUTING.md](../CONTRIBUTING.md#development): lint, coverage, the contracts, and the merge gate.
