# syntax=docker/dockerfile:1.6

# ----------------------------------------------------------------------------
# Stage 1: build all three binaries.
# ----------------------------------------------------------------------------
FROM golang:1.24-bookworm AS build

WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .

ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eux; \
    mkdir -p /out; \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
        go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/dredd        ./cmd/dredd; \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
        go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/dredd-build  ./cmd/dredd-build; \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
        go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/dreddagent   ./guest/dreddagent

# ----------------------------------------------------------------------------
# Stage 2: runtime image for the dredd API/worker.
#
# Only contains the dredd binary plus the dreddagent (so it can be extracted
# and injected into rootfs builds on another host). Does NOT include
# firecracker — that should be provided by the host bind-mount or sidecar.
# ----------------------------------------------------------------------------
FROM debian:bookworm-slim AS dredd

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/dredd      /usr/local/bin/dredd
COPY --from=build /out/dreddagent /usr/local/bin/dreddagent

ENV DREDD_HTTP_ADDR=:8080 \
    DREDD_LANGUAGES_FILE=/var/lib/dredd/languages.json \
    DREDD_ROOTFS_DIR=/var/lib/dredd/rootfs \
    DREDD_KERNEL_PATH=/var/lib/dredd/kernel/vmlinux \
    DREDD_AGENT_BINARY=/usr/local/bin/dreddagent

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/dredd"]

# ----------------------------------------------------------------------------
# Stage 3: builder image for dredd-build (one-time setup).
#
# Includes the docker CLI, mkfs.ext4 and mount, since dredd-build shells out
# to all of them. Must be run privileged with /var/run/docker.sock mounted.
# ----------------------------------------------------------------------------
FROM debian:bookworm-slim AS dredd-build

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        ca-certificates docker.io e2fsprogs mount util-linux tar \
 && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/dredd-build /usr/local/bin/dredd-build
COPY --from=build /out/dreddagent  /usr/local/bin/dreddagent

ENV DREDD_AGENT_BINARY=/usr/local/bin/dreddagent \
    DREDD_LANGUAGES_FILE=/var/lib/dredd/languages.json \
    DREDD_ROOTFS_DIR=/var/lib/dredd/rootfs

ENTRYPOINT ["/usr/local/bin/dredd-build"]
