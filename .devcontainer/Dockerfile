# syntax=docker/dockerfile:1

# A devcontainer with everything `make test` and `make contract-test` need.
#
# The Go base tag is the one version Docker cannot read from the repository
# before FROM: a base image must be a literal. It tracks the module's major
# line, and the build below fails if the image's Go is older than go.mod's
# floor, so go.mod remains the single Go pin. Rust and the stellar CLI are
# installed from the repo's own pins at build time.
FROM golang:1-bookworm

ARG TARGETARCH
ARG DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
	&& apt-get install -y --no-install-recommends \
		ca-certificates \
		curl \
		git \
		libdbus-1-3 \
		shellcheck \
	&& rm -rf /var/lib/apt/lists/*

# The repo is bind-mounted from the host, which is a different owner than the
# container's root. Without this, git (and therefore `go list`'s VCS stamp)
# refuses to read it as "dubious ownership".
RUN git config --global --add safe.directory /workspace

# go.mod's `go` line is the Go floor. Fail the build rather than produce a
# container that silently cannot build the module.
COPY go.mod /tmp/pins/go.mod
RUN set -eu; \
	want=$(awk '$1 == "go" {print $2; exit}' /tmp/pins/go.mod); \
	have=$(go env GOVERSION | sed 's/^go//'); \
	if [ "$(printf '%s\n%s\n' "$want" "$have" | sort -V | head -n1)" != "$want" ]; then \
		echo "devcontainer: image Go $have is older than go.mod's floor $want" >&2; \
		exit 1; \
	fi

# rustup reads the channel, components and targets from this file, so the
# contract toolchain (including wasm32v1-none) comes from the repo pin. Only the
# pin is copied into this layer, so it changes only when the pin does.
ENV RUSTUP_HOME=/usr/local/rustup \
	CARGO_HOME=/usr/local/cargo \
	PATH=/usr/local/cargo/bin:$PATH
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs \
		| sh -s -- -y --profile minimal --default-toolchain none \
	&& rm -rf /usr/local/rustup/downloads /usr/local/rustup/tmp
COPY assay-contracts/rust-toolchain.toml /tmp/rust-pin/rust-toolchain.toml
RUN cd /tmp/rust-pin && rustup toolchain install && rm -rf /tmp/rust-pin
WORKDIR /workspace

# The stellar CLI version is the one CONTRIBUTING.md records for the deployed
# build. Read it there rather than repeating it in this Dockerfile.
COPY CONTRIBUTING.md /tmp/pins/CONTRIBUTING.md
RUN set -eu; \
	version=$(awk -F'|' '/^\|[[:space:]]*`stellar` CLI[[:space:]]*\|/ {gsub(/[[:space:]]/, "", $3); print $3; exit}' /tmp/pins/CONTRIBUTING.md); \
	test -n "$version"; \
	case "${TARGETARCH:-amd64}" in \
		amd64) arch=x86_64-unknown-linux-gnu ;; \
		arm64) arch=aarch64-unknown-linux-gnu ;; \
		*) echo "devcontainer: unsupported architecture ${TARGETARCH}" >&2; exit 1 ;; \
	esac; \
	archive="stellar-cli-${version}-${arch}.tar.gz"; \
	curl -fsSL "https://github.com/stellar/stellar-cli/releases/download/v${version}/${archive}" \
		| tar -xz -C /usr/local/bin stellar; \
	rm -rf /tmp/pins; \
	stellar --version

# Warm the module cache so the first `make test` in the container is not
# dominated by downloads. The source is bind-mounted at run time; only the
# module files are baked in.
COPY go.mod go.sum /workspace/
RUN cd /workspace && go mod download
