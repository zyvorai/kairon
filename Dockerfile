# syntax=docker/dockerfile:1
FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/kairon-controller ./cmd/kairon-controller && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/kairon-node ./cmd/kairon-node && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/kaironctl ./cmd/kaironctl

FROM gcr.io/distroless/static-debian12:nonroot AS controller
COPY --from=build /out/kairon-controller /kairon-controller
USER 65532:65532
ENTRYPOINT ["/kairon-controller"]

FROM gcr.io/distroless/static-debian12:nonroot AS node
COPY --from=build /out/kairon-node /kairon-node
USER 65532:65532
ENTRYPOINT ["/kairon-node"]

FROM gcr.io/distroless/static-debian12:nonroot AS cli
COPY --from=build /out/kaironctl /kaironctl
USER 65532:65532
ENTRYPOINT ["/kaironctl"]
