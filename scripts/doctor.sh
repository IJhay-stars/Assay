#!/usr/bin/env bash
# Reports the local toolchain against the versions this repository pins.
#
# This is a read-only check: it never installs, upgrades or changes a tool. The
# pins it reads are the ones the rest of the repo already uses, so there is one
# place to change a version:
#
#   Go            the `go` directive in go.mod (a minimum, not an exact match)
#   rustc         the channel in assay-contracts/rust-toolchain.toml
#   wasm target   the targets in assay-contracts/rust-toolchain.toml
#   stellar CLI   the "Recorded build" table in CONTRIBUTING.md
#   ShellCheck    not version-pinned anywhere; CI uses the runner's copy at
#                 warning severity, so this only checks that it is installed
#
# Every line ends in OK or MISMATCH. A missing tool is a MISMATCH against its
# pin, never a pass. Exit 0 when every line is OK, 1 otherwise.
#
# Usage: scripts/doctor.sh   (or `make doctor`)

set -uo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root" || exit 1

mismatched=0

row() { # tool, expected, actual, ok(0/1)
	local status=OK
	if [[ $4 -ne 0 ]]; then
		status=MISMATCH
		mismatched=1
	fi
	printf '%-12s %-18s %-22s %s\n' "$1" "$2" "$3" "$status"
}

# version_matches ACTUAL PINNED
#
# A pin of "1.98" accepts "1.98.1"; a pin of "1.98.1" accepts only itself.
version_matches() {
	local actual=$1 pinned=$2
	[[ $actual == "$pinned" || $actual == "$pinned".* ]]
}

# at_least ACTUAL MINIMUM
#
# Compared field by field so the check works with BSD awk on macOS, where
# `sort -V` does not exist.
at_least() {
	awk -v actual="$1" -v minimum="$2" 'BEGIN {
		n = split(actual, a, ".")
		m = split(minimum, b, ".")
		for (i = 1; i <= n || i <= m; i++) {
			if (a[i] + 0 > b[i] + 0) exit 0
			if (a[i] + 0 < b[i] + 0) exit 1
		}
		exit 0
	}'
}

# --- Go ---------------------------------------------------------------------
# go.mod's `go` line is a floor: a newer toolchain builds the module, an older
# one is rejected (CI exports GOTOOLCHAIN=local so it cannot silently upgrade).
go_pin=$(awk '$1 == "go" {print $2; exit}' go.mod)
if [[ -z $go_pin ]]; then
	echo "doctor: cannot read the Go pin from go.mod" >&2
	exit 2
fi
go_actual=$(go version 2>/dev/null | awk '{print $3}' | sed 's/^go//')
if [[ -z $go_actual ]]; then
	row go ">=$go_pin" "not installed" 1
elif at_least "$go_actual" "$go_pin"; then
	row go ">=$go_pin" "$go_actual" 0
else
	row go ">=$go_pin" "$go_actual" 1
fi

# --- Rust (contract toolchain) ---------------------------------------------
# rustup reads assay-contracts/rust-toolchain.toml for commands run under that
# directory, so the versions below are the ones contract work will actually use.
rust_pin=$(awk -F'[="]' '$1 ~ /^[[:space:]]*channel[[:space:]]*$/ {print $3; exit}' \
	assay-contracts/rust-toolchain.toml)
if [[ -z $rust_pin ]]; then
	echo "doctor: cannot read the Rust channel from assay-contracts/rust-toolchain.toml" >&2
	exit 2
fi
rust_actual=$(cd assay-contracts && rustc --version 2>/dev/null | awk '{print $2}')
if [[ -z $rust_actual ]]; then
	row rustc "$rust_pin" "not installed" 1
elif version_matches "$rust_actual" "$rust_pin"; then
	row rustc "$rust_pin" "$rust_actual" 0
else
	row rustc "$rust_pin" "$rust_actual" 1
fi

# The wasm target decides whether `make contract-build` can produce the
# deployable artifact at all. Read the list from the pin, not from this script.
wasm_targets=()
while IFS= read -r target; do
	[[ -n $target ]] && wasm_targets+=("$target")
done < <(awk '
	/targets[[:space:]]*=/ { in_targets = 1 }
	in_targets {
		while (match($0, /"[^"]+"/)) {
			print substr($0, RSTART + 1, RLENGTH - 2)
			$0 = substr($0, RSTART + RLENGTH)
		}
		if (index($0, "]") > 0) exit
	}
' assay-contracts/rust-toolchain.toml)
if [[ ${#wasm_targets[@]} -eq 0 ]]; then
	echo "doctor: no targets declared in assay-contracts/rust-toolchain.toml" >&2
	exit 2
fi
installed_targets=$(cd assay-contracts && rustup target list --installed 2>/dev/null)
for target in "${wasm_targets[@]}"; do
	if grep -qx -- "$target" <<<"$installed_targets"; then
		row "target" "$target" "installed" 0
	else
		row "target" "$target" "missing" 1
	fi
done

# --- stellar CLI ------------------------------------------------------------
# CONTRIBUTING.md records the CLI version the deployed wasm was built with; the
# same value is quoted in docs/deployment.md. Read it rather than copying it.
stellar_pin=$(awk -F'|' '
	/^\|[[:space:]]*`stellar` CLI[[:space:]]*\|/ {
		gsub(/[[:space:]]/, "", $3); print $3; exit
	}
' CONTRIBUTING.md)
if [[ -z $stellar_pin ]]; then
	echo "doctor: cannot read the stellar CLI pin from CONTRIBUTING.md" >&2
	exit 2
fi
stellar_actual=$(stellar --version 2>/dev/null | awk 'NR == 1 {print $2}')
if [[ -z $stellar_actual ]]; then
	row stellar "$stellar_pin" "not installed" 1
elif version_matches "$stellar_actual" "$stellar_pin"; then
	row stellar "$stellar_pin" "$stellar_actual" 0
else
	row stellar "$stellar_pin" "$stellar_actual" 1
fi

# --- ShellCheck -------------------------------------------------------------
# CI runs `shellcheck -S warning` over every script. There is no pinned
# version, so any installed release is OK; absence is a mismatch.
shellcheck_actual=$(shellcheck --version 2>/dev/null | awk '/^version:/ {print $2; exit}')
if [[ -n $shellcheck_actual ]]; then
	row shellcheck "any" "$shellcheck_actual" 0
else
	row shellcheck "any" "not installed" 1
fi

exit "$mismatched"
