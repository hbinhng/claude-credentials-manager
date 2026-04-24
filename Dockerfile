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
# node:22-slim gives us the Claude Code CLI (distributed via npm)
# alongside ccm in the same image. The slim variant is debian-based
# and intentionally strips some system packages — we reinstate
# ca-certificates explicitly so Go's TLS can verify api.anthropic.com
# (OAuth refresh) and cloudflared's control plane.
#
# TARGETARCH is provided by the builder (Docker sets it automatically
# to the current platform's architecture, e.g. amd64 / arm64) so a
# bare `docker build` on either arch works without extra flags. For
# cross-arch builds use BuildKit / docker buildx with --platform.
FROM node:22-slim

ARG TARGETARCH=amd64
# Pin cloudflared to the same version ccm's EnsureCloudflared would
# have downloaded. Keeping them in lockstep means the on-PATH binary
# found at runtime is the one the Go side expects; bumping either
# side should bump the other.
ARG CLOUDFLARED_VERSION=2026.3.0

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl; \
    # Baking cloudflared into the image avoids a first-run network
    # fetch from github.com (which also sidesteps the "unknown
    # authority" TLS failures seen in some container environments
    # where the cert store was not yet warmed up). ccm's
    # EnsureCloudflared() resolves `cloudflared` on PATH before any
    # download attempt, so this binary is picked up directly.
    curl -fsSL --retry 3 --retry-delay 5 --retry-connrefused \
        -o /usr/local/bin/cloudflared \
        "https://github.com/cloudflare/cloudflared/releases/download/${CLOUDFLARED_VERSION}/cloudflared-linux-${TARGETARCH}"; \
    chmod 0755 /usr/local/bin/cloudflared; \
    /usr/local/bin/cloudflared --version; \
    apt-get purge -y --auto-remove curl; \
    rm -rf /var/lib/apt/lists/*; \
    npm install -g @anthropic-ai/claude-code; \
    npm cache clean --force

COPY --from=builder /out/ccm /usr/local/bin/ccm

# Dummy ~/.claude/.credentials.json so Claude Code can boot during
# `ccm share`'s capture step. The capture proxy records only the
# identity headers (User-Agent, X-Stainless-*, Anthropic-Version,
# Anthropic-Beta) — the Authorization header is NOT in the allowlist,
# so the bearer value here is ignored in flight. The value just has
# to exist so `claude -p` clears its own "have credentials" startup
# check and fires one request at the proxy. A far-future expiresAt
# prevents claude from trying to refresh a bogus refresh token.
#
# Real share traffic goes through the SERVING proxy, which reads the
# actual bearer from the ccm store at ~/.ccm/<uuid>.credentials.json
# and injects it into every forwarded request. This file is only the
# bootstrap for the capture subprocess.
RUN mkdir -p /root/.claude \
    && printf '%s' '{"claudeAiOauth":{"accessToken":"sk-ant-oat-capture-stub","refreshToken":"sk-ant-ort-capture-stub","expiresAt":9999999999999,"scopes":["user:inference"]}}' > /root/.claude/.credentials.json \
    && chmod 0600 /root/.claude/.credentials.json

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
