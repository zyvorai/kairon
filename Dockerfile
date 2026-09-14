# syntax=docker/dockerfile:1.7
# Copyright 2026 Zyvor · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0

# Every FROM is pinned to a digest, not just a tag: a floating tag can be
# repointed (accidentally or via supply-chain compromise) without this file
# changing at all. The tag stays alongside the digest for readability --
# Docker ignores it once a digest is present, it never affects what's
# actually pulled. These go stale as upstream ships security patches under
# the same tag, so they need periodic, deliberate refreshing (not
# "whenever CI happens to rebuild"); re-resolve with e.g.
#   docker buildx imagetools inspect <image>:<tag> | grep Digest
FROM node:22-bookworm-slim@sha256:83f487e0a63425e5b4d146fb5e5be574bcbe1b7b843d3ebafdd95eaf7767a7e5 AS web
WORKDIR /src
COPY web/package.json web/tsconfig.json web/vite.config.ts web/index.html ./web/
COPY web/src ./web/src
RUN cd web && npm install && npm run build

FROM golang:1.27-bookworm@sha256:648f440f42a0958804efb24df176f806f9d353b41f1c0627f666428e40310f6b AS build
WORKDIR /src
COPY go.mod ./
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/kairon-controller ./cmd/kairon-controller && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/kairon-node ./cmd/kairon-node && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/kairon-ui ./cmd/kairon-ui

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS controller
COPY --from=build /out/kairon-controller /kairon-controller
ENTRYPOINT ["/kairon-controller"]

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS node
COPY --from=build /out/kairon-node /kairon-node
ENTRYPOINT ["/kairon-node"]

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS ui
COPY --from=build /out/kairon-ui /kairon-ui
COPY --from=web /src/web/dist /web
ENV KAIRON_UI_WEB_DIR=/web
ENTRYPOINT ["/kairon-ui"]
