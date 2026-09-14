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
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/kairon-ui ./cmd/kairon-ui && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/kairon-csi-node ./cmd/kairon-csi-node && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/kairon-csi-controller ./cmd/kairon-csi-controller

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

# Deliberately NOT the distroless base every other Kairon image uses:
# internal/csinode shells out to a real iSCSI initiator (open-iscsi's
# iscsiadm) and filesystem tooling (util-linux's blkid, e2fsprogs' mkfs.*)
# it doesn't reimplement -- see internal/csinode/exec.go's own doc
# comment for why. A larger, real attack surface than the other three
# images, and a real tradeoff, not an oversight; it's also why this
# runs privileged (see charts/kairon/templates/all.yaml's kairon-csi-node
# DaemonSet) -- both are inherent to actually attaching/mounting network
# block devices from inside a container, not specific to this image.
FROM debian:12-slim@sha256:88200866dfff7ea7f5cbcb6ec7c8a701889efe6fe859fe64d6990e4b07ea4171 AS csi-node
RUN apt-get update && \
    apt-get install -y --no-install-recommends open-iscsi util-linux e2fsprogs ca-certificates && \
    rm -rf /var/lib/apt/lists/*
COPY --from=build /out/kairon-csi-node /kairon-csi-node
COPY docker/kairon-csi-node-entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]

# Also not distroless, for the same reason as csi-node above: dynamic
# provisioning (internal/csinode's ControllerServer) shells out to Linux
# LIO's targetcli to create/destroy real iSCSI backstores/targets/LUNs
# (see internal/csinode/lio.go) -- reimplementing raw configfs
# manipulation isn't something this project does either. targetcli-fb
# additionally needs the host's target_core_mod kernel module already
# loaded (an operator/host prerequisite this image deliberately doesn't
# try to modprobe itself -- see docs/guides/machine-storage-csi.md, the
# same "we don't reach into host-level setup for you" posture this
# project already takes for webhook/console TLS material).
FROM debian:12-slim@sha256:88200866dfff7ea7f5cbcb6ec7c8a701889efe6fe859fe64d6990e4b07ea4171 AS csi-controller
RUN apt-get update && \
    apt-get install -y --no-install-recommends targetcli-fb ca-certificates && \
    rm -rf /var/lib/apt/lists/*
COPY --from=build /out/kairon-csi-controller /kairon-csi-controller
ENTRYPOINT ["/kairon-csi-controller"]
