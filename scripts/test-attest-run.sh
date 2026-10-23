#!/usr/bin/env bash
# Tests for scripts/attest-run.sh: one run record per invocation for each
# outcome (attested, refused, errored), refusals recorded verbatim, nothing
# submitted unless derivation succeeded, and an empty run refused. The assay
# binary and the stellar CLI are stubs; nothing here talks to a network.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
SCRIPT="$ROOT/scripts/attest-run.sh"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

ASSET="USDC-GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN"
SAC="CA2E53VHFZ6YSWQIEIPBXJQGT6VW3VKWWZO555XKRQXYJ63GEBJJGHY7"
HASH="$(printf 'a%.0s' {1..64})"
TXHASH="$(printf 'b%.0s' {1..64})"

# Stub assay: prints $ASSAY_OUT and exits $ASSAY_RC; $ASSAY_ERR goes to stderr.
cat >"$TMP/assay" <<'STUB'
#!/usr/bin/env bash
if [ "${1:-}" != "attestation" ] || [ "${2:-}" != "-raw" ] || [ $# -ne 3 ]; then
    echo "stub assay: unsupported invocation: $*" >&2
    exit 64
fi
[ -n "${ASSAY_ERR:-}" ] && printf '%s\n' "$ASSAY_ERR" >&2
[ -n "${ASSAY_OUT:-}" ] && printf '%s\n' "$ASSAY_OUT"
exit "${ASSAY_RC:-0}"
STUB

# Stub stellar: `contract id asset` prints the SAC; `contract invoke` logs its
# arguments to $INVOKE_LOG and prints the signing line, or fails.
cat >"$TMP/stellar" <<'STUB'
#!/usr/bin/env bash
case "$*" in
    "contract id asset --asset USDC:GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN --network testnet")
        echo "CA2E53VHFZ6YSWQIEIPBXJQGT6VW3VKWWZO555XKRQXYJ63GEBJJGHY7" ;;
    "contract invoke "*)
        printf '%s\n' "$*" >>"$INVOKE_LOG"
        if [ "${INVOKE_RC:-0}" != 0 ]; then
            echo "error: transaction simulation failed: HostError: Error(Contract, #3)" >&2
            exit "$INVOKE_RC"
        fi
        echo "ℹ️  Signing transaction: $TXHASH" >&2
        echo "null" ;;
    *) echo "stub stellar: unsupported invocation: $*" >&2; exit 64 ;;
esac
STUB
chmod +x "$TMP/assay" "$TMP/stellar"

export ASSAY_BIN="$TMP/assay" STELLAR_BIN="$TMP/stellar" NETWORK=testnet \
    CONTRACT_ID=CBK4FBIHMDTXCUPE4E3ZDVSFJSCY5FJETTKNIQPN4LFJIKKIBLKIXQ73 \
    SOURCE=assay-attester INVOKE_LOG="$TMP/invoke.log" TXHASH

fail() { echo "FAIL: $*" >&2; exit 1; }

# run NAME [args...]: runs the script into $TMP/NAME.json, setting RC and OUT.
run() {
    local name=$1; shift
    : >"$INVOKE_LOG"
    set +e
    OUT=$("$SCRIPT" "$@" --record "$TMP/$name.json" 2>&1)
    RC=$?
    set -e
}

# field FILE EXPR: reads one value from a record with python3.
field() {
    python3 -c 'import json,sys; r=json.load(open(sys.argv[1])); print(eval(sys.argv[2]))' "$1" "$2"
}

# --- attested ---------------------------------------------------------------
ASSAY_OUT=$'2\t18\t'"$HASH" ASSAY_RC=0 run attested --asset "$ASSET"
[ "$RC" = 0 ] || fail "attested: exit $RC, want 0: $OUT"
[ "$(field "$TMP/attested.json" 'r["schema"]')" = "assay-attestation-run-v1" ] || fail "attested: schema"
[ "$(field "$TMP/attested.json" 'len(r["entries"])')" = 1 ] || fail "attested: want one entry"
[ "$(field "$TMP/attested.json" 'r["entries"][0]["status"]')" = attested ] || fail "attested: status"
[ "$(field "$TMP/attested.json" 'r["entries"][0]["tx_hash"]')" = "$TXHASH" ] || fail "attested: tx_hash"
[ "$(field "$TMP/attested.json" 'r["entries"][0]["sac"]')" = "$SAC" ] || fail "attested: sac"
[ "$(field "$TMP/attested.json" 'r["entries"][0]["severity"]')" = 2 ] || fail "attested: severity"
grep -q -- "--asset $SAC --severity 2 --flags 18 --evidence_hash $HASH" "$INVOKE_LOG" || fail "attested: submitted the wrong arguments: $(cat "$INVOKE_LOG")"
echo "ok   attested: record with tx_hash, submitted the derived arguments"

# --- refused, verbatim, nothing submitted ------------------------------------
REASON='assay: attestation: attest: scan is undetermined, so there is nothing to attest: reputation could not complete'
ASSAY_ERR="$REASON" ASSAY_RC=4 run refused --asset "$ASSET"
[ "$RC" = 1 ] || fail "refused: exit $RC, want 1"
[ "$(field "$TMP/refused.json" 'r["entries"][0]["status"]')" = refused ] || fail "refused: status"
[ "$(field "$TMP/refused.json" 'r["entries"][0]["reason"]')" = "$REASON" ] || fail "refused: reason not verbatim"
[ "$(field "$TMP/refused.json" 'r["entries"][0]["sac"]')" = "$SAC" ] || fail "refused: SAC not recorded"
[ ! -s "$INVOKE_LOG" ] || fail "refused: a refused asset was submitted"
echo "ok   refused: verbatim reason, SAC recorded, nothing submitted"

# --- a stale report is a refusal too ------------------------------------------
ASSAY_ERR='assay: attestation: attest: report is stale, so it cannot be attested as fresh: verdict is older than freshness policy window' ASSAY_RC=1 run stale --asset "$ASSET"
[ "$RC" = 1 ] || fail "stale: exit $RC, want 1"
echo "ok   stale: refused"

# --- errored at derivation: a scan failure is not a refusal -------------------
ASSAY_ERR='assay: horizon: get /assets: dial tcp: i/o timeout' ASSAY_RC=69 run scanerr --asset "$ASSET"
[ "$RC" = 2 ] || fail "scan error: exit $RC, want 2"
[ "$(field "$TMP/scanerr.json" 'r["entries"][0]["status"]')" = errored ] || fail "scan error: status"
[ ! -s "$INVOKE_LOG" ] || fail "scan error: submitted anyway"
echo "ok   scan error: errored, nothing submitted"

# --- errored at submission ----------------------------------------------------
ASSAY_OUT=$'4\t48\t'"$HASH" ASSAY_RC=0 INVOKE_RC=1 run submiterr --asset "$ASSET"
[ "$RC" = 2 ] || fail "submit error: exit $RC, want 2"
[ "$(field "$TMP/submiterr.json" 'r["entries"][0]["status"]')" = errored ] || fail "submit error: status"
field "$TMP/submiterr.json" 'r["entries"][0]["reason"]' | grep -q 'Error(Contract, #3)' || fail "submit error: reason lost"
echo "ok   submit error: errored with the CLI's error recorded"

# --- reasons with quotes, tabs and newlines stay valid JSON --------------------
NASTY=$'assay: attest: scan is undetermined: "quoted"\tand\nsplit \\ line'
ASSAY_ERR="$NASTY" ASSAY_RC=4 run nasty --asset "$ASSET"
[ "$(field "$TMP/nasty.json" 'r["entries"][0]["reason"]')" = "$NASTY" ] || fail "escaping: reason did not round-trip"
echo "ok   escaping: reason round-trips through JSON"

# --- an empty run is an error, and writes no record ---------------------------
run empty
[ "$RC" = 2 ] || fail "empty run: exit $RC, want 2"
printf '%s' "$OUT" | grep -q "empty run" || fail "empty run: message does not say so: $OUT"
[ ! -e "$TMP/empty.json" ] || fail "empty run: wrote a record with zero entries"
echo "ok   empty run: refused, no record"

echo "attest-run: all tests passed"
