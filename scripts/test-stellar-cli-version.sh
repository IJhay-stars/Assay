#!/usr/bin/env bash
# Tests the Makefile's Stellar CLI version guard with a stub binary.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TMPDIR=$(mktemp -d)
STELLAR="$TMPDIR/stellar"

cleanup() {
    rm -rf "$TMPDIR"
}
trap cleanup EXIT

cat >"$STELLAR" <<'STUB'
#!/usr/bin/env bash
if [ "${STELLAR_VERSION:-}" = "missing" ]; then
    exit 1
fi
printf 'stellar %s\n' "${STELLAR_VERSION:-}"
STUB
chmod +x "$STELLAR"

fail() {
    echo "FAIL: $1" >&2
    printf '%s\n' "$OUTPUT" >&2
    exit 1
}

run_rejected() {
    local target="$1"
    local version="$2"
    OUTPUT=""
    if OUTPUT=$(env STELLAR_VERSION="$version" PATH="$TMPDIR:$PATH" \
        make -s --no-print-directory -C "$ROOT" "$target" 2>&1); then
        fail "$target accepted stellar $version"
    fi
    [[ "$OUTPUT" == *"expected stellar CLI 27.1.0"* ]] ||
        fail "$target did not report the expected CLI version"
    [[ "$OUTPUT" == *"Install stellar CLI 27.1.0 and retry."* ]] ||
        fail "$target did not provide an install instruction"
}

OUTPUT=$(env STELLAR_VERSION=27.1.0 PATH="$TMPDIR:$PATH" \
    make -s --no-print-directory -C "$ROOT" check-stellar-cli-version 2>&1) ||
    fail "check rejected the pinned version"

for target in check-stellar-cli-version build-contract deploy-testnet attest read; do
    run_rejected "$target" 28.1.0
done

run_rejected check-stellar-cli-version 127.1.0
run_rejected check-stellar-cli-version missing

printf '%s\n' "test-stellar-cli-version: all checks passed"
