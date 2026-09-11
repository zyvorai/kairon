# syntax=docker/dockerfile:1.7
# Copyright 2026 Zyvor · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0

FROM node:22-bookworm-slim AS web
WORKDIR /src
COPY web/package.json web/tsconfig.json web/vite.config.ts web/index.html ./web/
COPY web/src ./web/src
RUN cd web && npm install && npm run build

FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/kairon-controller ./cmd/kairon-controller && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/kairon-node ./cmd/kairon-node && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/kairon-ui ./cmd/kairon-ui

FROM gcr.io/distroless/static-debian12:nonroot AS controller
COPY --from=build /out/kairon-controller /kairon-controller
ENTRYPOINT ["/kairon-controller"]

FROM gcr.io/distroless/static-debian12:nonroot AS node
COPY --from=build /out/kairon-node /kairon-node
ENTRYPOINT ["/kairon-node"]

FROM gcr.io/distroless/static-debian12:nonroot AS ui
COPY --from=build /out/kairon-ui /kairon-ui
COPY --from=web /src/web/dist /web
ENV KAIRON_UI_WEB_DIR=/web
ENTRYPOINT ["/kairon-ui"]
