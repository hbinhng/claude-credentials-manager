# syntax=docker/dockerfile:1.7

# ccm in a container. The image ships a single statically linked Go
# binary at /usr/local/bin/ccm and boots `ccm serve --bind-host 0.0.0.0`
# as its entrypoint, so a bare `docker run` gives you the web
# dashboard on :7878 against whichever credential store is mounted at
# /root/.ccm.
#
# Usage:
#   docker build -t ccm .
#   docker run --rm -p 7878:7878 \
#       -e CCM_SERVE_TOKEN=$(openssl rand -base64 24) \
#       -v ~/.ccm:/root/.ccm \
#       ccm

# ---------- builder -----------------------------------------------
# golang:1.24-alpine keeps the builder image small (~200 MB) and
# gives us the `make`, `git`, and `bash` toolchain the repo's
# Makefile expects. CGO is already off in the environment so the
# resulting binary is a pure static ELF that drops cleanly into any
# scratch/distroless final image.
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git make bash

WORKDIR /src

# Prime the Go module cache from go.mod/go.sum first so source edits
# don't bust dependency layers on every rebuild.
COPY go.mod go.sum ./
RUN go mod download

# Build context (.dockerignore trims the noise). Keeping .git in the
# context is deliberate — the Makefile reads `git rev-parse` for the
# COMMIT ldflag; without it the binary reports "unknown".
COPY . .

# Delegate to the canonical Makefile so the link-time flags match
# exactly what `make` produces on a developer machine (VERSION from
# npm/package.json, COMMIT from git, BUILD_DATE from the host clock).
# `make` writes ./ccm; promote it to a known path for the final COPY.
RUN mkdir -p /out && make && cp ccm /out/ccm

# ---------- runtime -----------------------------------------------
# distroless/static has no shell and no package manager — only libc-
# less userspace plus ca-certificates (needed for the TLS calls ccm
# makes to the Anthropic OAuth endpoints). We deliberately use the
# root-capable variant (not :nonroot) because the expected run-time
# pattern is `docker run -v ~/.ccm:/root/.ccm ...` and matching UIDs
# across host and container gets awkward without root; callers who
# need non-root can `docker run --user 65532:65532 -e HOME=...` and
# bind-mount a pre-chowned store.
FROM gcr.io/distroless/static-debian12

COPY --from=builder /out/ccm /usr/local/bin/ccm

# ccm reads and writes ~/.ccm/<uuid>.credentials.json plus the PID
# file at ~/.ccm/serve.pid. HOME=/root for this image, so bind-mount
# the host store there.
EXPOSE 7878

# --bind-host 0.0.0.0 makes the dashboard reachable on every
# interface inside the container's network namespace, which is what
# you want when publishing a port. Any non-loopback bind triggers
# mandatory token auth: set CCM_SERVE_TOKEN to a stable value, or
# let ccm auto-generate one and read it from `docker logs`.
ENTRYPOINT ["/usr/local/bin/ccm", "serve", "--bind-host", "0.0.0.0"]
