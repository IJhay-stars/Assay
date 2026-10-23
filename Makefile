BINARY := assay
PKG := ./...

# `make` with no target prints the target list; every target carries a `##`
# description that `make help` reads.
.DEFAULT_GOAL := help

CONTRACTS := assay-contracts
WASM := $(CONTRACTS)/out/assay_safety_registry.wasm

# Deployment coordinates. CONTRACT_ID is the live testnet deployment recorded in
# docs/deployment.md; override it to point the attest/read targets elsewhere.
NETWORK ?= testnet
SOURCE ?= assay-attester
CONTRACT_ID ?= CBK4FBIHMDTXCUPE4E3ZDVSFJSCY5FJETTKNIQPN4LFJIKKIBLKIXQ73
STELLAR_CLI_VERSION := 27.1.0

.PHONY: all build build-release test test-notify test-preimage test-reproducibility test-stellar-cli-version cover lint fmt vet run clean \
	offline-test e2e-standalone \
	contract-test contract-lint contract-build \
	build-contract deploy-testnet attest read check-stellar-cli-version verify-gate verify-wasm \
	eval-record eval-compare

all: build ## Build the scanner (same as build)

build: ## Build the scanner into ./assay
	go build -o $(BINARY) ./cmd/assay

# build-release builds the scanner under the flags that make the binary
# reproducible from any directory on any machine:
#
#   -trimpath      removes absolute file system paths from the binary
#   -buildvcs=false removes the working tree's VCS state (a dirty checkout
#                  would otherwise stamp vcs.modified=true)
#   CGO_ENABLED=0  removes the host C toolchain as an input
#
# The Go toolchain is pinned by go.mod, so the two inputs left are the source
# and the flags. CI proves the property by building this target twice in
# different directories and comparing hashes; see CONTRIBUTING.md
# ("Reproducible scanner builds").
#
# Note: this does not stamp a version. A build from a checkout reports
# ScannerIdentity "devel"; tagged releases are built by
# .github/workflows/release.yml with `go install ...@<tag>`, which stamps the
# tag. That split is deliberate: reproducibility of the local build and
# version binding of a release are separate guarantees.
build-release: ## Build the scanner reproducibly (trimpath, no VCS stamp, CGO off)
	CGO_ENABLED=0 go build -trimpath -buildvcs=false -o $(BINARY) ./cmd/assay

test: ## Run the Go tests with -race and -shuffle=on
	go test -race -shuffle=on $(PKG)

# test-notify exercises scripts/notify-scheduled-failure.sh against a stubbed
# gh, covering open/update/comment/close and the "exactly one issue" rule. CI
# runs it alongside the merge-gate tests.
test-notify: ## Test the scheduled-failure notifier against a stubbed gh
	bash ./scripts/test-notify-scheduled-failure.sh

# test-reproducibility exercises scripts/reproducibility.sh against a stubbed
# assay binary and fixture tables, covering the PASS/MISMATCH/ENCODING/
# INCONCLUSIVE outcomes, their exit-code precedence, and the refusal of a
# table without a valid Preimage column. It needs no network, so CI runs it
# even though the script it tests does.
test-reproducibility: ## Test the reproducibility script offline against fixtures
	bash ./scripts/test-reproducibility.sh

# test-attest-run exercises scripts/attest-run.sh (the run record `make attest`
# writes) against stubbed assay and stellar binaries. No network, no key.
test-attest-run:
	bash ./scripts/test-attest-run.sh

# test-stellar-cli-version exercises the Makefile preflight with a stub CLI.
test-stellar-cli-version:
	bash ./scripts/test-stellar-cli-version.sh

# test-preimage runs the independent Python encoder (written from
# docs/contract-interface.md alone) against the Go encoder over every committed
# vector, every internal/mechanics/testdata subject, and the language-neutral
# conformance cases. CI runs this as its own job; a deliberate encoding change
# fails here until the specification and both implementations agree.
test-preimage: ## Cross-check the evidence preimage against the Python encoder
	go test ./internal/attest/ -run IndependentEncoder -v

# e2e-standalone joins the scanner and the contracts on a local quickstart
# network (issue #463): fixture-derived Params are submitted with the stellar
# CLI, then get_safety and the example gate are asserted. CI starts the network
# with stellar/quickstart; locally, run `stellar container start local` first.
# The script refuses a non-loopback RPC URL and never contacts a public network.
e2e-standalone: ## Run the scanner and contracts against a local quickstart network
	./scripts/e2e-standalone.sh

# cover prints the same per-package table CI puts in the job summary
# (scripts/coverage-report.sh), lowest coverage first. It reports; it does not
# gate. The test exit status is preserved so a failing test still fails.
cover: ## Print the per-package coverage table, lowest first
	@go test -covermode=atomic -coverprofile=coverage.out $(PKG) > coverage.log 2>&1; \
	rc=$$?; cat coverage.log; echo; \
	./scripts/coverage-report.sh coverage.log coverage.out; \
	exit $$rc

fmt: ## Format the Go source with gofmt
	gofmt -w .

vet: ## Run go vet over every package
	go vet $(PKG)

lint: ## Run golangci-lint
	golangci-lint run

run: build ## Build and start the API on :8080
	./$(BINARY) serve

contract-test: ## Run the contract tests with cargo test
	cd $(CONTRACTS) && cargo test

contract-lint: ## Check contract formatting and run clippy with warnings denied
	cd $(CONTRACTS) && cargo fmt --all -- --check
	cd $(CONTRACTS) && cargo clippy --all-targets -- -D warnings

contract-build: ## Compile the contracts to wasm without the stellar optimizer
	cd $(CONTRACTS) && cargo build --release --target wasm32v1-none

# Fail before invoking a CLI version that may produce incompatible output or wasm.
check-stellar-cli-version:
	@actual="$$(stellar --version 2>/dev/null || true)"; \
	if ! printf '%s\n' "$$actual" | grep -Eq '(^|[^0-9])$(subst .,[.],$(STELLAR_CLI_VERSION))([^0-9]|$$)'; then \
		[ -n "$$actual" ] || actual='not installed'; \
		echo "error: expected stellar CLI $(STELLAR_CLI_VERSION), found '$$actual'." >&2; \
		echo "Install stellar CLI $(STELLAR_CLI_VERSION) and retry." >&2; \
		exit 1; \
	fi

# build-contract produces the deployable artifact. It goes through the stellar
# CLI rather than cargo because the CLI also runs the wasm optimizer and checks
# the exported interface; contract-build above is the plain compile.
build-contract: check-stellar-cli-version
	stellar contract build --manifest-path $(CONTRACTS)/Cargo.toml \
		--package assay-safety-registry --out-dir $(CONTRACTS)/out

# verify-wasm rebuilds each contract from the committed source and checks the
# result against the wasm hashes recorded in docs/deployment.md. It builds into
# a temporary directory, so it never disturbs the artifact deploy-testnet would
# upload, and it never edits the document or submits a transaction.
#
# It exits 0 only when every recorded hash is reproduced. Until
# assay-contracts/rust-toolchain.toml exists (#127) the toolchain is whatever
# rustup has installed, which does not determine the bytes, so the run reports
# UNVERIFIABLE and exits 2. That is the honest answer, not a failure of the
# source. See docs/deployment.md ("Does the source still build what is
# deployed?").
verify-wasm:
	./scripts/verify-wasm-source.sh $(if $(SOURCE_REF),--source-ref "$(SOURCE_REF)",)

deploy-testnet: build-contract ## Deploy the contract to testnet and print the new ID
	stellar contract deploy --wasm $(WASM) \
		--source-account $(SOURCE) --network $(NETWORK)
	@echo
	@echo "Deployed. Record the contract ID in docs/deployment.md, then run:"
	@echo "  stellar contract invoke --id <ID> --source-account $(SOURCE) \\"
	@echo "    --network $(NETWORK) -- init --admin \$$(stellar keys address $(SOURCE))"

# attest scans ASSET live and writes the result on-chain.
#
# Every number submitted comes from `assay attestation`, which derives severity,
# the mechanic bitset, and evidence_hash from that scan. Nothing here lets a
# hand-written value reach the contract.
# attest writes one run record per invocation under attest-runs/ (see
# docs/attestation-writer.md, "The run record"), whatever the outcome.
attest: check-stellar-cli-version build
	@ASSAY_BIN=./$(BINARY) NETWORK=$(NETWORK) CONTRACT_ID=$(CONTRACT_ID) SOURCE=$(SOURCE) \
		bash ./scripts/attest-run.sh --asset '$(ASSET)'

# read calls get_safety against the deployed contract. It simulates rather than
# submits, so reading an attestation costs nothing and needs no signature.
read: check-stellar-cli-version
	@test -n "$(ASSET)" || { echo 'usage: make read ASSET=CODE-ISSUER'; exit 2; }
	@set -eu; \
	sac=$$(stellar contract id asset --asset "$$(printf '%s' '$(ASSET)' | sed 's/-/:/')" --network $(NETWORK)); \
	echo "$(ASSET) -> $$sac"; \
	stellar contract invoke --id $(CONTRACT_ID) --source-account $(SOURCE) --network $(NETWORK) \
		--send=no -- get_safety --asset "$$sac"

# Runs the merge gate against a commit range, the same script CI runs on every
# PR. Verify a change offline before opening the PR:
#   make verify-gate BASE=main HEAD=HEAD
verify-gate: ## Run the merge gate over BASE..HEAD locally
	@test -n "$(BASE)" || { echo 'usage: make verify-gate BASE=<sha-or-ref> HEAD=<sha-or-ref>'; exit 2; }
	./scripts/merge-gate.sh "$(BASE)" "$(HEAD)"

test-gate: ## Test the merge-gate script against fixture commit ranges
	bash ./scripts/test-merge-gate.sh

# Records the labelled corpus's classification, per subject and per check.
# Commit the result when the classifier's output is intended to change; it is
# the baseline eval-compare diffs against.
eval-record: ## Record the labelled corpus's classification as the eval baseline
	go run ./cmd/eval -out docs/eval-baseline.json

# Re-captures one corpus fixture from the URLs recorded in PROVENANCE.md and
# prints a diff, so a maintainer can judge whether a moved verdict reflects a
# real change or a regression. It needs network access, so it is deliberately
# not part of `make test` or CI. A fetch that fails writes nothing.
#
#   make refresh-fixture SUBJECT=aqua-clear-verified
#   make refresh-fixture SUBJECT=aqua-clear-verified DRY_RUN=1
refresh-fixture: ## Re-capture one corpus fixture and print its diff
	@test -n "$(SUBJECT)" || { echo 'usage: make refresh-fixture SUBJECT=<fixture-dir>'; exit 2; }
	go run ./cmd/refreshfixture -subject "$(SUBJECT)" $(if $(DRY_RUN),-dry-run,)

# Compares the current classifier against the recorded baseline and reports
# what moved: severity, mechanics and evidence separately, with undetermined
# subjects and corpus membership changes reported on their own. Exits 0 on a
# movement so it can be read as a report; add STRICT=1 to make movement fail.
eval-compare: ## Compare the classifier against the recorded eval baseline
	@go run ./cmd/eval -compare docs/eval-baseline.json $(if $(STRICT),-strict,)

# Prints the confusion matrix over the labelled corpus, showing agreements
# and disagreements per severity level and per check with undetermined as
# its own outcome class. Sample size is printed with every result.
# Precision and recall require -precision-recall and carry a sample-size
# caveat.
eval: ## Print the confusion matrix and precision/recall over the corpus
	@go run ./cmd/eval -confusion -precision-recall

clean: ## Remove build, coverage and contract output
	rm -f $(BINARY) coverage.out coverage.html coverage.log
	rm -rf $(CONTRACTS)/out
	cd $(CONTRACTS) && cargo clean
