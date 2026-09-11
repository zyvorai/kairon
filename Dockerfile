# syntax=docker/dockerfile:1.7
FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/kairon-controller ./cmd/kairon-controller && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/kairon-node ./cmd/kairon-node

FROM gcr.io/distroless/static-debian12:nonroot AS controller
COPY --from=build /out/kairon-controller /kairon-controller
ENTRYPOINT ["/kairon-controller"]

FROM gcr.io/distroless/static-debian12:nonroot AS node
COPY --from=build /out/kairon-node /kairon-node
ENTRYPOINT ["/kairon-node"]
