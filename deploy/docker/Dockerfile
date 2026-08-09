# Server Assistant — Unraid-host Docker image (issue #52: Docker container
# via Community Applications, primary packaging vehicle).
#
# Multi-stage: static Go binary, no CGO, no language runtime in the final
# image. This mirrors ADR 0004's "one static binary" rule (docs/DEPLOY.md) —
# the container is a distribution/CA convenience, not a departure from it.
# Assets are already embedded via embed.FS at compile time (ADR 0004), so
# there is nothing else to COPY into the final stage.

# Builder pinned to an exact patch tag, never `latest` or a bare floating
# `1.22` tag — CONVENTIONS' dependency-minimalism table exists to stop
# exactly this kind of silent drift. Matches go.mod's `go 1.22` line.
FROM golang:1.22.12-bookworm AS builder

WORKDIR /src

# Cache module downloads separately from source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# internal/store/db/ is generated, gitignored, and excluded from the build
# context by .dockerignore — so it must be produced here rather than copied.
# Without this the image only builds on a machine that happens to have run
# `make sqlc` already, which is exactly the kind of works-on-my-box
# reproducibility hole CONVENTIONS' pinned-tooling rule exists to close.
# Version matches the Makefile's SQLC_VERSION pin; bumping one means bumping
# both in the same commit.
RUN go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.27.0 \
    && sqlc generate

# CGO_ENABLED=0 (CONVENTIONS rule 1) + -trimpath (no build-machine paths
# leaked into the binary) + stripped symbols (-s -w) for a smaller static
# binary. GOARCH is amd64 to match rijkaardserver.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/server-assistant \
    ./cmd/server-assistant

# ---------------------------------------------------------------------------
# Final stage.
#
# Alpine, not scratch/distroless, for two reasons:
#  1. docker-compose.yml's HEALTHCHECK needs an in-container HTTP client, and
#     a scratch/distroless image ships no shell or client at all (BusyBox
#     wget is the smallest thing that answers).
#  2. The script dry-run executor (internal/scripts) is not pure Go — it
#     shells out to `unshare` to build the sandbox, `strace` to get
#     ground-truth execve tracing for rejection-predicate branch (b), and
#     `bash` to run the script itself. Without these three the executor
#     fails closed and no script can ever be approved, so they are load-
#     bearing, not conveniences.
# This is a size/tooling tradeoff, not a security one — per issue #52, the
# boundary that actually holds for this stack is the human Approval gate,
# hash-bound argument-free scripts, and the mandatory dry run (see
# docker-compose.yml), not how minimal the base image is.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget bash util-linux strace \
    && addgroup -S server-assistant \
    && adduser -S -G server-assistant -H -D server-assistant \
    && mkdir -p /var/lib/server-assistant \
    && chown server-assistant:server-assistant /var/lib/server-assistant

# The SQLite/audit data directory must exist in the IMAGE and be owned by the
# runtime user: Docker seeds an empty named volume from the image path it is
# mounted over, ownership included. Create it only at run time and the volume
# is seeded root-owned, the non-root process cannot open the database, and the
# container crash-loops on startup.
VOLUME ["/var/lib/server-assistant"]

COPY --from=builder /out/server-assistant /usr/local/bin/server-assistant

USER server-assistant
# Container-internal port stays at the app's untouched default (:8080).
# docker-compose.yml remaps only the host-facing publish to 8099, because
# 8080 is already bound on rijkaardserver by an unrelated container
# (docs/research/mcp-reachability.md §2).
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/server-assistant"]
CMD ["-config", "/etc/server-assistant/config.yaml"]
