# ADR 0008 agent-dev sandbox image: a frozen, reproducible toolchain with the
# full CONVENTIONS dependency graph prebaked into the module cache, so every
# slice runs with ZERO network and ZERO secrets (CONVENTIONS rule 9).
#
# This image prebakes the toolchain and cache only — it never contains the
# repo. Copy-in happens at run time (tools/agent/run.sh), never a bind-mount:
# a botched run must not be able to reach the host tree (ADR 0008).
#
# Dev-time meta-tooling. Never compiled into nor shipped with the single
# static binary (CONVENTIONS rule 1; ADR 0008).
#
# Rebuild deliberately when the CONVENTIONS dependency table or a pinned tool
# version changes — in the SAME commit as that change (see README.md).

# Pinned to the host Go 1.22 line; bumping is a deliberate Go-upgrade decision.
FROM golang:1.22.2-bookworm

# Tool versions: keep in lockstep with the Makefile pins (single bump point).
ARG SQLC_VERSION=v1.27.0
ARG GOOSE_VERSION=v3.21.1
ARG GOLANGCI_LINT_VERSION=v1.59.1

# `make` for the gate targets; `git` (scm variant has it, pin explicitly) for
# branch-out. No network tools — the sandbox is offline by construction.
RUN apt-get update \
 && apt-get install -y --no-install-recommends make git ca-certificates \
 && rm -rf /var/lib/apt/lists/*

ENV CGO_ENABLED=0

# Prebake the pinned Go dev tools into /go/bin (already on PATH). Built from
# source under this exact Go toolchain, so the versions are reproducible.
RUN go install github.com/sqlc-dev/sqlc/cmd/sqlc@${SQLC_VERSION} \
 && go install github.com/pressly/goose/v3/cmd/goose@${GOOSE_VERSION} \
 && go install github.com/golangci/golangci-lint/cmd/golangci-lint@${GOLANGCI_LINT_VERSION}

# Prebake the entire module graph from the committed, pinned go.mod/go.sum
# (ARK-5 / Slice 0). `download all` warms the full graph — every
# CONVENTIONS-table dependency and transitive — not just build-time modules,
# so build + test + lint all work offline for every later slice.
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download all

# Warm the std build cache too: compiling the standard library once here means
# a copied-in repo needs no network and minimal CPU to build/vet/test.
RUN go build std

# Fail-closed AFTER the cache is warm: any module cache miss now errors
# instead of silently reaching the network, even if a run is misconfigured
# without --network=none (CONVENTIONS rule 9; ADR 0008).
ENV GOPROXY=off GOFLAGS=-mod=mod

# Copy-in lands here at run time (tools/agent/run.sh). Empty in the image.
WORKDIR /workspace
CMD ["bash"]
