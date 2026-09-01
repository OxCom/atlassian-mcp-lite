# syntax=docker/dockerfile:1
# The syntax directive is required for RUN --mount=type=secret below.

# One named stage for the base image, so the tag resolves exactly once. Without
# it the final stage's COPY --from=golang:1.27-trixie is a second, independent
# resolution, and a tag that moved mid-build would put the toolchain and the
# runtime trust roots in different images.
FROM golang:1.27-trixie AS base

# Build the static binary.
FROM base AS build

# Module source. Defaults are the public ones, so a plain `docker build` needs
# no arguments. compose.override.yaml supplies Artifactory values on a
# corporate network.
ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org
ARG GOPRIVATE=
ENV GOPROXY=${GOPROXY} GOSUMDB=${GOSUMDB} GOPRIVATE=${GOPRIVATE}

# The release tag, stamped into the binary. Defaults to empty, in which case the
# built-in default in internal/core stands.
ARG VERSION=
# The build context excludes .git (see .dockerignore), so stamping VCS
# information would fail the build rather than omit it.
ENV GOFLAGS=-buildvcs=false

WORKDIR /src
COPY go.mod go.sum ./

# Both secrets are optional. When none is passed the mounts are empty, the
# guards do nothing, and the build behaves exactly as it does outside the
# office. Secrets are used rather than build args so neither the CA nor the
# Artifactory token is written into a layer or shown by `docker history`.
# The CA is used WITHOUT being installed. Appending it to the system bundle
# would put the interception CA into the layer this stage produces, and the
# final stage copies that bundle — so a container built on the office network
# would trust the corporate CA for its Atlassian TLS connection. Measured: the
# runtime bundle grew by exactly that certificate. Instead the combined bundle
# lives in /tmp, is named through SSL_CERT_FILE, and is deleted inside the same
# RUN so it never becomes part of a layer. Only `go mod download` needs the
# network; the later build does not.
RUN --mount=type=secret,id=corp_ca,target=/run/secrets/corp_ca \
    --mount=type=secret,id=netrc,target=/root/.netrc \
    set -eu; \
    if [ -s /run/secrets/corp_ca ]; then \
        cat /etc/ssl/certs/ca-certificates.crt /run/secrets/corp_ca > /tmp/bundle.crt; \
        export SSL_CERT_FILE=/tmp/bundle.crt; \
    fi; \
    go mod download; \
    rm -f /tmp/bundle.crt

COPY . .
# CGO off and a stripped binary so the final image can be scratch. Version is
# an -X into internal/core, where it lives; package main has no such symbol and
# the linker would ignore an -X naming one, silently shipping the default.
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w ${VERSION:+-X github.com/OxCom/atlassian-mcp-lite/internal/core.Version=${VERSION}}" \
    -o /out/atlassian-mcp-lite ./cmd/atlassian-mcp-lite

# Final image: no shell, no package manager, no libc.
FROM scratch
# TLS roots, needed to verify the Atlassian certificate. Taken from a pristine
# image rather than from the build stage, so no corporate CA can reach the
# runtime trust store even if a future edit installs one during the build.
COPY --from=base /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/atlassian-mcp-lite /atlassian-mcp-lite
# Non-root. scratch has no /etc/passwd, so the id is given numerically.
USER 65532:65532
ENTRYPOINT ["/atlassian-mcp-lite"]
