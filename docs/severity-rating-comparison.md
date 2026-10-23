# Assay Severity and StellarExpert Rating

Comparison captured 2026-10-22. This is a descriptive comparison, not a
calibration or a claim that either scale predicts the other.

## What the values mean

Assay severity reports issuer capability, with a separate upward-only
reputation escalation. StellarExpert's `rating.average` is its own 0-10 score
based on its published asset metrics; the API also returns age, activity,
trustlines, liquidity, 7-day volume, and interoperability components. The
average is recorded as published, not calculated by Assay.

The top-50 membership comes from the committed
[`stellar-expert-top50.json`](../internal/assetlist/testdata/stellar-expert-top50.json),
captured 2026-09-27. Ratings below were queried from StellarExpert on
2026-10-22. The top-50 API page returned 42 matching Stellar Asset Contract
(SAC) addresses; the other eight fixture assets had moved outside that page and
were queried by their per-asset endpoint. Assets are matched by SAC, not ticker,
because the list contains duplicate codes. The table labels each asset with its
code and first eight issuer characters; the fixture contains the full issuer.

The 18 scan subjects are those in [attestation-run.md](attestation-run.md).
They were re-scanned live on 2026-10-22 between 14:23:40Z and 14:24:00Z. The
remaining top-50 assets were scanned during the same UTC date. A scan with
`undetermined: true` is not a severity result; a refused scan is likewise not
assigned a severity. The rating and scan snapshots are close in time, but are
not atomic and both sources can change.

## Top-50 fixture

All 50 fixture assets returned a StellarExpert average, ranging from 6.8 to
9.7, with a mean of 7.71. Their distribution is 1 asset at 6.0-6.9, 34 at
7.0-7.9, 11 at 8.0-8.9, and 4 at 9.0-10. All 50 were scanned: 41 completed
with a determinate result and nine remained undetermined after one retry. The
completed results were 32 clear, 2 medium, and 7 high; none was critical.

| Asset / issuer prefix | StellarExpert average / 10 | Assay result |
| --- | ---: | --- |
| yXLM / GARDNV3Q | 9.2 | clear |
| XRP / GBXRPL45 | 9.2 | clear |
| yUSDC / GDGTVWSM | 9.7 | clear |
| USDC / GA5ZSEJY | 9.0 | medium |
| BTCLN / GDPKQ2TS | 8.7 | clear |
| AQUA / GBNZILST | 8.8 | clear |
| BTC / GDPJALI4 | 8.2 | clear |
| yBTC / GBUVRNH4 | 8.3 | clear |
| SSLX / GBHFGY3Z | 8.3 | clear |
| SHX / GDSTRSHX | 8.2 | clear |
| ARS / GCYE7C77 | 8.0 | clear |
| yETH / GDYQNEF2 | 8.0 | clear |
| IDRT / GDPKQ2TS | 8.0 | clear |
| ETH / GBFXOHVA | 8.0 | clear |
| ZARZ / GAROH4EV | 8.0 | high |
| ARST / GCSAZVWX | 7.8 | clear |
| USDZ / GAKTLPC4 | 7.8 | high |
| USTRY / GCRYUGD5 | 7.8 | high |
| CETES / GCRYUGD5 | 7.8 | high |
| XCHF / GDPKQ2TS | 7.7 | clear |
| MTL / GACKTN5D | 7.7 | clear |
| EURMTL / GACKTN5D | 7.7 | clear |
| USDM / GDHDC4GB | 7.7 | clear |
| PYUSD / GDQE7IXJ | 7.7 | high |
| USD / GDUKMGUG | 7.5 | clear |
| CLPX / GDYSPBVZ | 7.5 | clear |
| PEN / GA4TDPNU | 7.5 | clear |
| EURC / GAQRF3UG | 7.5 | clear |
| EURC / GDHU6WRG | 7.5 | medium |
| XRP / GBVOL67T | 7.3 | clear |
| BTC / GBVOL67T | 7.3 | clear |
| XXA / GC4HS4CQ | 7.3 | clear |
| SCOP / GC6OYQJI | 7.3 | clear |
| VELO / GDM4RQUQ | 7.3 | undetermined |
| AFR / GBX6YI45 | 7.3 | clear |
| UAF / GDT5EUZM | 7.3 | clear |
| STARDUST / GCPLNANL | 7.2 | clear |
| USDY / GAJMPX5N | 7.3 | high |
| MOBI / GA6HCMBL | 7.2 | clear |
| ETH / GBVOL67T | 7.2 | clear |
| LTC / GBVOL67T | 7.2 | clear |
| XTAR / GAORYJ3K | 7.2 | undetermined |
| KRW / GDPKQ2TS | 7.0 | undetermined |
| SATSMTL / GACKTN5D | 7.2 | undetermined |
| USDGLO / GBBS25EG | 7.2 | high |
| sUSD / GCHW7CWI | 7.2 | undetermined |
| TFT / GBOVQKJY | 7.0 | undetermined |
| BRAVE / GDREF4TA | 7.0 | undetermined |
| LSP / GAB7STHV | 7.0 | undetermined |
| GOLD / GBC5ZGK6 | 6.8 | undetermined |

## Other scanned subjects

These six subjects from the existing 18-asset sample are outside the top-50
fixture. StellarExpert returned no `rating.average` for entries marked `not
rated`; zero is a returned score, not a missing value.

| Asset / issuer prefix | StellarExpert average / 10 | Assay result |
| --- | ---: | --- |
| BERKSHIRE / GA22QHSH | not rated | critical (base high) |
| DOGE / GA22IDJN | 0.0 | critical (base clear) |
| KALE / GAKJM27Q | not rated | clear |
| KALE / GBEQXW5H | not rated | critical (base clear) |
| REPO / GA2BVQLG | 3.2 | critical (base clear) |
| yFLR / GAIUD5NP | 0.0 | refused: Horizon issuer account returned 404 |

## What diverges

- **High rating does not mean low issuer capability.** USDC scores 9.0 and is
  `medium` because its issuer can freeze balances. USDZ scores 7.8 and is
  `high` because its issuer has revocable and clawback flags. Their ratings do
  not change those capability findings.
- **Rating ranges overlap across completed severity levels.** The mean
  StellarExpert averages were 7.85 for clear (32 assets), 8.25 for medium (2),
  and 7.66 for high (7). The small, selected sample cannot support a
  correlation or predictive claim, but it shows why one scale cannot stand in
  for the other.
- **The top-50 list does not include the strongest reputation escalations.**
  None of its 50 assets was critical in this scan. Outside the list, DOGE has
  rating 0 and final severity `critical`, but its base capability severity is
  `clear`; the escalation comes from an attributed malicious-asset signal.
  REPO has rating 3.2 and is `critical` by its blocklisted issuer domain.
  BERKSHIRE and the scam KALE issuer are also `critical` without a returned
  average rating. These are not independent validation of StellarExpert:
  Assay's final severity can consume directory and blocklist claims, but never
  the numeric rating.
- **Incomplete evidence stays incomplete.** Nine top-50 scans remained
  `undetermined` after one retry, with an unavailable reputation check; their
  apparent `clear` base result is not presented as a complete severity. yFLR's
  issuer account was unavailable, so no severity was assigned.

These observations support the distinction between the measures, not a
correlation claim. The sample is selected, ratings and scans can change, and
StellarExpert's own fixture description says reported fraudulent assets are
removed from its Top 50. No rating is fed into severity, and severity logic is
unchanged.

## Sources

- StellarExpert Top 50 membership fixture: captured 2026-09-27; see
  [`internal/assetlist/testdata/PROVENANCE.md`](../internal/assetlist/testdata/PROVENANCE.md).
- StellarExpert ratings: `GET https://api.stellar.expert/explorer/public/asset?order=desc&limit=100&cursor=0`
  and per-asset lookups for fixture assets outside the first 100 records;
  queried 2026-10-22.
- Assay scans: `assay scan CODE-ISSUER`, live on public network on 2026-10-22.