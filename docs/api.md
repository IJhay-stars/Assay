# Assay HTTP API

`assay serve` exposes one HTTP surface: the scan API, the observation history,
the exposure page, the operational probes, and a single-file UI.

**Two references exist, with one job each:**

- [`openapi.yaml`](../openapi.yaml) at the repository root is the **schema
  source**. It is machine-readable, it is held to the routes the server
  actually registers by `TestOpenAPICoversAllRegisteredRoutes`, and its
  component schemas are held to the fields the handlers actually emit by
  `TestOpenAPISchemasMatchHandlerEmittedFields`. Generate clients from it.
- **This page** is the human reference for the same surface: what each
  endpoint is for, what it answers, and what it refuses. It documents every
  endpoint below; when the two disagree about a field name or a status code,
  the OpenAPI document wins and this page is the bug.

## Conventions

- Every response body is JSON, utf-8 encoded, except `GET /` (HTML) and
  `GET /healthz` (plain text).
- Errors are `{"error": "<message>"}` with the status code doing the
  classification; no endpoint signals failure with `200` and a message.
- The scan response **is** the report, unwrapped. It carries `schema_version`,
  the payload contract version, which is independent of the `/api/v1` route
  version: a CLI consumer never sees the route, so the report carries its own
  contract. [api-versioning.md](api-versioning.md) states what each version
  promises.
- A field missing from a response is absent, not `null` and not an error;
  consumers must not require fields the additive-change policy is allowed to
  add later.

### The native asset

The native asset (XLM) has no issuer account, so there are no authorization
flags to read and nothing to classify. An identifier that names it — `native`
or `XLM`, in any case, with no issuer half — is answered with
`400 Bad Request` and an explicit message, never a parse error and never a
report:

```json
{ "error": "native asset has no issuer and no authorization flags: \"XLM\" cannot be scanned or attested" }
```

It is refused by design: the native asset is never scanned and never attested,
so it stays exactly what [integrating.md](integrating.md) already uses it as —
a never-attested asset the on-chain gate refuses. `GET /api/v1/history?asset=XLM`
returns the same `400` and message. A credit asset whose code merely happens
to be XLM (`XLM-G…`) is an ordinary asset and scans normally.

## Endpoints

### `GET /api/v1/scan?asset=CODE-ISSUER`

Scans an asset and returns its capability report. Each successful scan also
records one observation into the history store (see
[history.md](history.md)).

Parameters:

- `asset` (required): asset code and issuer, e.g. `USDC-GA5Z...`.
- `holder` (optional): a holder's `G...` address. Adds the holder-trustline
  finding — whether the issuer can freeze or confiscate *this holder's*
  balance — to the report.

The parameter accepts the same three spellings as the CLI — `CODE-ISSUER`,
`CODE:ISSUER`, and a stellar.expert asset URL (percent-encoded) — and every
response names the asset in its canonical `CODE-ISSUER` form, so evidence and
preimage bytes do not depend on which form was sent. See
[cli.md](cli.md#asset-identifiers).

Response headers:

- `X-Assay-Undetermined`: set to `true` when the scan is degraded by an
  upstream source outage (such as a failure to reach StellarExpert). When
  `true`, the body contains `undetermined: true` and `undetermined_checks`,
  meaning the source data was incomplete and the severity should be treated
  with caution. Always present; `false` on a complete scan.

Status codes:

- `200 OK`: the scan report. Check `X-Assay-Undetermined` or the body's
  `undetermined` field for source degradation.
- `400 Bad Request`: invalid asset code or issuer, missing `?asset=`, or an
  identifier naming the native asset — see above.
- `404 Not Found`: asset not found on the ledger.
- `502 Bad Gateway`: the scan failed on an underlying network or upstream
  error; the message names the failing source.

#### Checking one holder's exposure: `holder=`

`GET /api/v1/scan?asset=CODE-ISSUER&holder=G...`

Without `holder`, a scan answers **"would I be exposed?"**: what the issuer can
do to a new holder, from the issuer's current flags. With `holder`, it also
answers **"am I exposed?"** for that one account. Under CAP-0035,
`is_clawback_enabled` is fixed on a trustline when the trustline is created,
so a holder who opened theirs before the issuer enabled clawback is not
exposed to it, even if the issuer's flag is set now. Issuer flags cannot tell
those two holders apart; the holder's own trustline can.

**Validation.** `holder` must be a Stellar account ID (`G…`, 56 characters).
Anything else is refused with `400 Bad Request` before any fetch:

```json
{ "error": "scan: invalid holder account ID: \"not-an-account\"" }
```

An empty `holder=` is the same as leaving it out.

**What changes in the report.** The scan reads the holder's trustline from
Horizon (`/accounts/{holder}`) and adds one finding, `check: "trustline"`
("Holder trustline flags"):

| Holder's trustline | Finding | Report |
| --- | --- | --- |
| `is_clawback_enabled: true` | Evidence quoting both trustline flags, and mechanic `trustline_clawback_enabled` | `mechanics` gains `trustline_clawback_enabled` |
| Not authorized (`is_authorized: false`) | Mechanic `trustline_deauthorized` | `mechanics` gains `trustline_deauthorized` |
| Authorized, no clawback | Evidence quoting both flags, no mechanic; the reasoning explains CAP-0035 grandfathering | Unchanged |
| The holder does not hold the asset | `undetermined: true`: there is no trustline to inspect | `undetermined: true`, `undetermined_checks` includes `trustline`, and the `X-Assay-Undetermined: true` header |
| Horizon could not be reached for the holder | `undetermined: true`, with a `not retrievable:` evidence claim | As above |

**`severity` never changes.** Severity is issuer-level and holder-independent
by design, so the same asset reports the same severity to every caller and an
on-chain gate reads a stable number. The holder answer is in the `trustline`
finding and in the two `trustline_*` mechanics only.

**Not for attestation.** A holder scan adds holder-specific evidence, so its
`evidence_hash` differs from the plain scan's, and a holder who does not hold
the asset makes the report undetermined. Attestations are always derived from
plain scans (`assay attestation` takes no holder).

Example: a holder whose trustline predates the issuer's clawback flag.

```sh
curl -s 'http://localhost:8080/api/v1/scan?asset=USDZ-GAKTLPC4ZV37SSCITQ5IS5AQ4WPF4CF4VZJQPPAROSGXMYOATF5U6XPR&holder=GBRPYHIL2CI3FNQ4BXLFMNDLFJUNPU2HY3ZMFSHONUCEOASW7QC7OX2H'
```

```json
{
  "severity": "high",
  "mechanics": ["auth_revocable", "auth_clawback_enabled"],
  "undetermined": false,
  "findings": [
    {
      "check": "trustline",
      "title": "Holder trustline flags",
      "severity": "clear",
      "undetermined": false,
      "mechanics": [],
      "reasoning": "Holder GBRP… has an authorized trustline with is_clawback_enabled=false. …"
    }
  ]
}
```

(Abridged: the report's other fields and findings are unchanged by `holder`.)
To check every trustline an account holds at once, use the
exposure endpoint (`GET /api/v1/exposure`, below).

### `GET /api/v1/history?asset=CODE-ISSUER`

Returns an asset's recorded observations in time order, oldest first, with
the transitions derived between each consecutive pair. It reads only what
scans have recorded; it never scans. The full semantics — retention, what an
empty history means, and why an undetermined observation is returned marked
rather than dropped — are in [history.md](history.md).

Parameters:

- `asset` (required): asset code and issuer.

Response:

- `asset`, `state` (`valid`, `unknown`, or `missing` — the completeness of
  the history, not of the request), a plain-language `reason` whenever the
  state is not `valid`, `count`, `observations[]`, `transitions[]`,
  `latest_at` and `latest_age_secs` (so a consumer applies its own staleness
  policy), and `retention` (the per-asset cap the results were read against).

Status codes:

- `200 OK`: history returned — including an empty one. An asset with no
  observations is a successful result with `state: "missing"`, never a 404:
  "we have never looked" and "the request failed" are different answers.
- `400 Bad Request`: missing or invalid asset, or the network's native asset.

### `GET /api/v1/exposure?account=G...&limit=10&cursor=0`

Lists the trustlines an account holds and answers, per asset, with the asset's
issuer-level severity and its holder-trustline finding — the per-wallet form
of `?holder=` on the scan endpoint. **There is no aggregate or portfolio
score:** every asset carries its own severity and its own undetermined flag,
and an undetermined asset says nothing about any other asset on the page.

Parameters:

- `account` (required): the holder's `G...` address.
- `limit` (optional): page size, default `10`, clamped to a maximum of `50`.
  One page is up to `limit` full scans, which is why the bound exists.
- `cursor` (optional): index of the first trustline to return, for the next
  page. `0` starts; a cursor at or past the end returns an empty page.

Response:

- `account`, `total` (trustlines on the account), `limit` (page size applied),
  `count` (assets in this page), and `next_cursor` — absent on the last page.
- `assets[]`: per asset, `asset` (`code`/`issuer`), `severity`,
  `undetermined`, `undetermined_checks`, the `finding` from the holder-
  trustline check, and `reason` when that asset's scan failed outright (the
  `finding` is then absent rather than rendered as a clear answer).

Status codes:

- `200 OK`: page returned. Individual assets may be undetermined — see each
  entry's `undetermined` field; there is no header equivalent for a page.
- `400 Bad Request`: missing or invalid `account`, `limit`, or `cursor`.
- `404 Not Found`: account not found on the ledger.
- `429 Too Many Requests`: rate limited. `Retry-After` gives the wait in
  seconds. Exposure is rate-limited per client address because one request
  runs a full scan per asset; `/api/v1/scan` is not affected.
- `502 Bad Gateway`: the account's trustline list could not be fetched.

### `GET /metrics`

Scan outcome and per-source failure counters accumulated since process start:
`scan_outcomes` (counts per class: `complete`, `undetermined`, `not_found`,
`upstream_failure`) and `source_failures` (counts per upstream source that
did not answer). The counters are monotonically increasing; a scraper derives
rates from consecutive reads.

Status codes:

- `200 OK`: a snapshot of the counters. Both objects are present and may be
  empty on a fresh process.

### `GET /healthz`

Liveness. Reports only that the process is up; it checks no upstream, and a
failing probe must never take it down — a dying process must still be able to
answer.

Status codes:

- `200 OK`: the process is running. Body: plain text `ok`.

### `GET /readyz`

Per-upstream readiness. It asks each checkable upstream (Horizon, and the
consumed sources) whether it is reachable, and reports the answer per source
so an operator can read *why* from the body. The HTTP status tracks the
body's `status`: `200` when ready, `503` otherwise. No failing probe ever
reports ready.

Response:

- `status`: `ready`, `degraded` (a consumed source is unreachable — scans are
  still served, but the reputation axis may be undetermined), `unavailable`
  (Horizon is unreachable — scans cannot succeed at all), or `unknown`.
- `reason`: present when the status is not `ready`.
- `checked_at` and `age_secs`: when the reported probe round actually ran and
  how old it is; the answer is cached, so it may predate this request.
- `upstreams[]`: per source, `name`, `kind` (`horizon` or `consumed`),
  `reachable`, `probe_error` (the failure verbatim, when unreachable), and
  `probe_url` (so the probe is reproducible from the report alone).

Status codes:

- `200 OK`: every checkable upstream is reachable.
- `503 Service Unavailable`: degraded, unavailable, or unknown; see `status`
  and `upstreams` for which and why.

### `GET /`

Serves the single-file UI: one embedded HTML page over the same API, for a
human with a browser. It is not a client-generator target and carries no
contract beyond being HTML; the API endpoints above are the interface.

Status codes:

- `200 OK`: the HTML page. Any other path is a `404`.

## Related documents

- [`openapi.yaml`](../openapi.yaml) — the schema source for this surface.
- [api-versioning.md](api-versioning.md) — what `/api/v1` and
  `schema_version` each promise, and the additive-change rule.
- [history.md](history.md) — observation history semantics, retention, and
  the meaning of every state.
- [integrating.md](integrating.md) — how a consumer reads reports out of this
  API and gates on them.
- [cli.md](cli.md) — the same reports from the command line.
