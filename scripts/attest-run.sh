#!/usr/bin/env bash
# One manual attestation run, as `make attest` drives it, and its run record.
#
# Derives the attest() arguments with `assay attestation -raw`, derives the
# asset's SAC address for the registry's network, submits, and writes the
# machine-readable run record specified in docs/attestation-writer.md
# ("The run record"). The record is written whatever happened: a run that was
# refused or failed is exactly the run whose record matters.
#
#   scripts/attest-run.sh --asset CODE-ISSUER [--record PATH]
#
# Environment (the Makefile sets these):
#   ASSAY_BIN    the assay binary            (default ./assay)
#   STELLAR_BIN  the stellar CLI             (default stellar)
#   NETWORK      registry network            (default testnet)
#   CONTRACT_ID  registry contract ID        (required)
#   SOURCE       signing identity            (default assay-attester)
#   RECORD_DIR   where records go by default (default attest-runs)
#
# Exit codes mirror the dry-run's: 0 attested, 1 refused, 2 errored or usage.
# A run with no asset is an empty run, and an empty run is an error (exit 2):
# no record is written, because a record with zero entries is invalid.

set -uo pipefail

ASSAY_BIN=${ASSAY_BIN:-./assay}
STELLAR_BIN=${STELLAR_BIN:-stellar}
NETWORK=${NETWORK:-testnet}
CONTRACT_ID=${CONTRACT_ID:-}
SOURCE=${SOURCE:-assay-attester}
RECORD_DIR=${RECORD_DIR:-attest-runs}

ASSET=""
RECORD=""
while [ $# -gt 0 ]; do
    case "$1" in
        --asset) ASSET=${2:-}; shift 2 ;;
        --record) RECORD=${2:-}; shift 2 ;;
        *) echo "attest-run: unknown argument $1" >&2; exit 2 ;;
    esac
done

if [ -z "$ASSET" ]; then
    echo "attest-run: empty run: no asset given (usage: make attest ASSET=CODE-ISSUER). A run record with zero entries is an error." >&2
    exit 2
fi
if [ -z "$CONTRACT_ID" ]; then
    echo "attest-run: CONTRACT_ID is not set" >&2
    exit 2
fi

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }
STARTED=$(now)
RUN_ID=$(date -u +%Y%m%dT%H%M%SZ)
if [ -z "$RECORD" ]; then
    RECORD="$RECORD_DIR/run-$RUN_ID-${ASSET%%-*}.json"
fi

# json_str renders a JSON string. Reasons are recorded verbatim, so every
# character that JSON needs escaped is escaped rather than dropped; other
# control characters (which no tool here emits) are removed.
json_str() {
    local s
    s=$(printf '%s' "$1" | tr -d '\000-\010\013\014\016-\037')
    s=${s//\\/\\\\}
    s=${s//\"/\\\"}
    s=${s//$'\t'/\\t}
    s=${s//$'\r'/\\r}
    s=${s//$'\n'/\\n}
    printf '"%s"' "$s"
}
json_or_null() { if [ -n "$1" ]; then json_str "$1"; else printf 'null'; fi; }

STATUS=""
REASON=""
SEVERITY=""
FLAGS=""
HASH=""
SAC=""
TX_HASH=""

# 1. Derive. A refusal is attest's own verdict on the report; anything else
#    that stops the derivation is an error.
params=$("$ASSAY_BIN" attestation -raw "$ASSET" 2>"${TMPDIR:-/tmp}/attest-run.$$.err")
rc=$?
derive_err=$(cat "${TMPDIR:-/tmp}/attest-run.$$.err" 2>/dev/null)
rm -f "${TMPDIR:-/tmp}/attest-run.$$.err"
if [ $rc -ne 0 ]; then
    REASON=$derive_err
    case "$derive_err" in
        *"scan is undetermined"*|*"never evaluated"*|*"report is stale"*|*"confiscation invariant"*)
            STATUS=refused ;;
        *)
            STATUS=errored ;;
    esac
else
    SEVERITY=$(printf '%s' "$params" | cut -f1)
    FLAGS=$(printf '%s' "$params" | cut -f2)
    HASH=$(printf '%s' "$params" | cut -f3 | tr -d ' \r\n')
fi

# 2. The SAC address, for the registry's network. Derived even for a refused
#    asset, so the record names the address that was not written.
sac_out=$("$STELLAR_BIN" contract id asset --asset "${ASSET/-/:}" --network "$NETWORK" 2>&1)
if [ $? -eq 0 ]; then
    SAC=$(printf '%s' "$sac_out" | tail -n 1 | tr -d ' \r\n')
elif [ -z "$STATUS" ]; then
    STATUS=errored
    REASON="derive SAC address: $sac_out"
fi

# 3. Submit, only if nothing above stopped the run.
if [ -z "$STATUS" ]; then
    echo "$ASSET"
    echo "  sac          $SAC"
    echo "  severity     $SEVERITY"
    echo "  flags        $FLAGS"
    echo "  evidence     $HASH"
    submit_out=$("$STELLAR_BIN" contract invoke --id "$CONTRACT_ID" --source-account "$SOURCE" --network "$NETWORK" \
        -- attest --asset "$SAC" --severity "$SEVERITY" --flags "$FLAGS" --evidence_hash "$HASH" 2>&1)
    rc=$?
    printf '%s\n' "$submit_out"
    if [ $rc -ne 0 ]; then
        STATUS=errored
        REASON=$submit_out
    else
        STATUS=attested
        # The CLI prints the submitted transaction's hash; the last 64-hex
        # token in its output is it. A success without one is still recorded
        # as attested, with tx_hash null, rather than guessed.
        TX_HASH=$(printf '%s' "$submit_out" | grep -oE '\b[0-9a-f]{64}\b' | tail -n 1)
    fi
fi

FINISHED=$(now)
COMMIT=$(git rev-parse HEAD 2>/dev/null || true)

mkdir -p "$(dirname "$RECORD")"
{
    printf '{\n'
    printf '  "schema": "assay-attestation-run-v1",\n'
    printf '  "run_id": %s,\n' "$(json_str "$RUN_ID")"
    printf '  "started_at": %s,\n' "$(json_str "$STARTED")"
    printf '  "finished_at": %s,\n' "$(json_str "$FINISHED")"
    printf '  "mode": "manual",\n'
    printf '  "network": %s,\n' "$(json_str "$NETWORK")"
    printf '  "contract_id": %s,\n' "$(json_str "$CONTRACT_ID")"
    printf '  "source_account": %s,\n' "$(json_str "$SOURCE")"
    printf '  "commit": %s,\n' "$(json_or_null "$COMMIT")"
    printf '  "entries": [\n'
    printf '    {\n'
    printf '      "asset": %s,\n' "$(json_str "$ASSET")"
    printf '      "sac": %s,\n' "$(json_or_null "$SAC")"
    printf '      "status": %s,\n' "$(json_str "$STATUS")"
    if [ -n "$SEVERITY" ]; then
        printf '      "severity": %s,\n' "$SEVERITY"
        printf '      "flags": %s,\n' "$FLAGS"
        printf '      "evidence_hash": %s,\n' "$(json_str "$HASH")"
    fi
    if [ "$STATUS" = attested ]; then
        printf '      "tx_hash": %s\n' "$(json_or_null "$TX_HASH")"
    else
        printf '      "reason": %s\n' "$(json_str "$REASON")"
    fi
    printf '    }\n'
    printf '  ]\n'
    printf '}\n'
} >"$RECORD"
echo "attest-run: $STATUS, record written to $RECORD" >&2

case "$STATUS" in
    attested) exit 0 ;;
    refused) [ -n "$REASON" ] && echo "$REASON" >&2; exit 1 ;;
    *) [ -n "$REASON" ] && echo "$REASON" >&2; exit 2 ;;
esac
