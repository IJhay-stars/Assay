#!/usr/bin/env bash
# Weekly differential check over the attested set (issue #302).
#
# Re-runs the independent ledger derivation for every asset in the
# docs/deployment.md attested-assets table and reports the state from
# docs/differential.md. The check is deliberately about disagreement between
# two ledger routes, not about evidence freshness: `verify` may also report a
# hash mismatch (#24), so this script reads the differential state from its
# JSON rather than treating the whole command's exit code as the outcome.
#
# Usage:
#   scripts/differential.sh [--bin PATH] [--deployment PATH] [--rpc URL]
#
#   --bin PATH         assay binary to run (default: $ASSAY_BIN or ./assay).
#   --deployment PATH  markdown file holding the attested-assets table
#                      (default: docs/deployment.md relative to the repo root).
#   --rpc URL          Soroban RPC endpoint passed to `verify -differential`.
#                      Default: the binary's default (SDF testnet).
#
# Exit codes:
#   0  every asset reached differential agreement (PASS).
#   1  at least one asset disagreed (DISAGREEMENT), or a usage/table error.
#      A disagreement is a deterministic finding and always fails the run.
#   2  no disagreement, but at least one asset was inconclusive (one side or
#      both sides produced no reading, or the command produced no differential
#      result). An upstream outage is not agreement and not a hard failure;
#      the workflow reports it as a warning.
#
# A run can produce both outcomes; disagreement outranks inconclusive, matching
# the reproducibility job's precedence.

set -u
set -o pipefail

BIN="${ASSAY_BIN:-./assay}"
DEPLOYMENT=""
RPC=""

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

usage() {
    sed -n '2,/^$/p' "$SCRIPT_DIR/differential.sh" | sed 's/^# \{0,1\}//' >&2
    echo "usage: $0 [--bin PATH] [--deployment PATH] [--rpc URL]" >&2
}

while [ $# -gt 0 ]; do
    case "$1" in
        --bin) BIN="${2:-}"; shift 2 ;;
        --deployment) DEPLOYMENT="${2:-}"; shift 2 ;;
        --rpc) RPC="${2:-}"; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "differential: unknown argument $1" >&2; usage; exit 1 ;;
    esac
done

if [ -z "$DEPLOYMENT" ]; then
    DEPLOYMENT="$REPO_ROOT/docs/deployment.md"
fi

if [ ! -x "$BIN" ] && ! command -v "$BIN" >/dev/null 2>&1; then
    if [ ! -x "$REPO_ROOT/$BIN" ] && [ ! -x "$BIN" ]; then
        echo "differential: assay binary not found: $BIN (build with: go build -o assay ./cmd/assay)" >&2
        exit 1
    fi
fi
if [ ! -f "$DEPLOYMENT" ]; then
    echo "differential: deployment file not found: $DEPLOYMENT" >&2
    exit 1
fi

# Parse the attested-assets table. Its rows are the ones whose second column
# is a Stellar issuer (G...), whose first column is the asset code, and which
# carry the SAC/hash/encoding columns; that is the same table
# scripts/reproducibility.sh reads. Requiring those columns matters: the live
# addresses table also has a G... address in its second column ("Admin /
# attester"), and that is an identity, not an asset. Emit CODE-ISSUER lines.
if ! ASSETS="$(python3 - "$DEPLOYMENT" <<'PY'
import re, sys
path = sys.argv[1]
issuer_re = re.compile(r'^G[A-Z2-7]{55}$')
with open(path, encoding='utf-8') as f:
    for line in f:
        if not line.startswith('|'):
            continue
        cols = [c.strip().strip('`').strip() for c in line.strip().strip('|').split('|')]
        if len(cols) < 4:
            continue
        code, issuer = cols[:2]
        if code and issuer_re.match(issuer):
            print(f"{code}-{issuer}")
PY
)"; then
    echo "differential: failed to parse attested assets from $DEPLOYMENT" >&2
    exit 1
fi
if [ -z "$ASSETS" ]; then
    echo "differential: parsed 0 attested assets from $DEPLOYMENT; the table format may have changed" >&2
    exit 1
fi

N_ASSETS="$(printf '%s\n' "$ASSETS" | wc -l | tr -d ' ')"
echo "differential: $N_ASSETS attested assets from $DEPLOYMENT" >&2
echo "differential: binary $BIN${RPC:+, rpc $RPC}" >&2

rpc_args=()
if [ -n "$RPC" ]; then
    rpc_args=(-rpc "$RPC")
fi

agreement_count=0
disagreement_count=0
inconclusive_count=0
disagreements=""
inconclusives=""

while IFS= read -r asset; do
    [ -n "$asset" ] || continue
    errfile="$(mktemp)"
    # The assay CLI times out internally after 60s; `timeout` is a backstop so
    # one hung asset cannot stall the whole scheduled run.
    if command -v timeout >/dev/null 2>&1; then
        out="$(timeout 90 "$BIN" verify -differential -json "${rpc_args[@]}" "$asset" 2>"$errfile")" && rc=0 || rc=$?
    else
        out="$("$BIN" verify -differential -json "${rpc_args[@]}" "$asset" 2>"$errfile")" && rc=0 || rc=$?
    fi
    detail="$(head -c 500 "$errfile" | tr '\n' ' ')"
    rm -f "$errfile"

    # The differential object is present even when verify exits non-zero for a
    # hash mismatch or an inconclusive hash outcome. Missing it means the
    # command failed before the differential check could report anything.
    parsed=""
    if [ -n "$out" ]; then
        parsed="$(printf '%s' "$out" | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin).get("differential") or {}
except Exception:
    d = {}
state = d.get("state") or ""
reason = (d.get("reason") or "").replace("\t", " ")
if state:
    print(state + "\t" + reason)
' 2>/dev/null)" || true
    fi
    state="${parsed%%$'\t'*}"
    reason=""
    if [ -n "$parsed" ] && [ "$parsed" != "$state" ]; then
        reason="${parsed#*$'\t'}"
    fi

    case "$state" in
        agreement)
            agreement_count=$((agreement_count + 1))
            echo "PASS $asset"
            ;;
        disagreement)
            disagreement_count=$((disagreement_count + 1))
            disagreements="${disagreements}${asset} :: ${reason:-both ledger routes answered with different flags}"$'\n'
            echo "DISAGREEMENT $asset :: ${reason:-both ledger routes answered with different flags}"
            ;;
        inconclusive_one_side|inconclusive_both_sides)
            inconclusive_count=$((inconclusive_count + 1))
            inconclusives="${inconclusives}${asset} :: ${state}${reason:+ — $reason}"$'\n'
            echo "INCONCLUSIVE $asset :: $state${reason:+ — $reason}"
            ;;
        *)
            inconclusive_count=$((inconclusive_count + 1))
            msg="${detail:-verify produced no differential result (rc=$rc)}"
            inconclusives="${inconclusives}${asset} :: ${msg}"$'\n'
            echo "INCONCLUSIVE $asset :: ${msg}"
            ;;
    esac
done <<< "$ASSETS"

echo "" >&2
echo "differential: $agreement_count agreement, $disagreement_count disagreement, $inconclusive_count inconclusive (of $N_ASSETS)" >&2

if [ "$disagreement_count" -gt 0 ]; then
    echo "" >&2
    echo "DISAGREEMENTS (both ledger routes answered, differently — investigate; neither value is chosen):" >&2
    printf '%s' "$disagreements" >&2
    echo "result: FAIL" >&2
    exit 1
fi

if [ "$inconclusive_count" -gt 0 ]; then
    echo "" >&2
    echo "INCONCLUSIVE assets (one or both derivations produced no reading — not failures, not passes):" >&2
    printf '%s' "$inconclusives" >&2
    echo "An upstream outage or missing ledger entry is not a differential disagreement. Re-run when sources recover." >&2
    echo "result: INCONCLUSIVE" >&2
    exit 2
fi

echo "result: PASS — all $N_ASSETS differential reads agree" >&2
exit 0
