# Copyright 2026 Zyvor · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0

SHELL := /bin/sh
GO ?= go
IMAGE ?= ghcr.io/zyvorai/kairon
TAG ?= dev
VERSION ?= $(shell cat VERSION)
LDFLAGS := -s -w -X main.version=$(VERSION)
COVERAGE_THRESHOLD ?= 50

.PHONY: all fmt fmt-check vet lint test test-race cover cover-check build clean validate docker-build smoke
all: fmt-check vet lint test-race cover-check build validate smoke

fmt:
	gofmt -w $$(find cmd internal -name '*.go' -type f)

fmt-check:
	test -z "$$(gofmt -l cmd internal)"

vet:
	$(GO) vet ./...

lint:
	golangci-lint run ./...

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

cover:
	$(GO) test -coverprofile=coverage.out ./internal/...

cover-check: cover
	@$(GO) tool cover -func=coverage.out | tee /dev/stderr | tail -1 | \
	awk -v m=$(COVERAGE_THRESHOLD) '{ pct=$$3; sub("%","",pct); \
	if (pct+0 < m) { printf "coverage %.1f%% is below threshold %d%%\n", pct, m; exit 1 } \
	else { printf "coverage %.1f%% meets threshold %d%%\n", pct, m } }'

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/kairon-controller ./cmd/kairon-controller
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/kairon-node ./cmd/kairon-node
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/kaironctl ./cmd/kaironctl
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/kairon-ui ./cmd/kairon-ui

smoke: build
	test "$$($(CURDIR)/bin/kaironctl version)" = "$(VERSION)"
	test "$$($(CURDIR)/bin/kairon-controller --version)" = "$(VERSION)"
	test "$$($(CURDIR)/bin/kairon-node --version)" = "$(VERSION)"
	test "$$($(CURDIR)/bin/kairon-ui --version)" = "$(VERSION)"

validate:
	python3 scripts/validate.py

docker-build:
	docker build --target controller -t $(IMAGE)-controller:$(TAG) .
	docker build --target node -t $(IMAGE)-node:$(TAG) .

dist: all
	mkdir -p dist
	tar --exclude='./dist' --exclude='./bin' -czf dist/kairon-$(VERSION)-src.tar.gz .

clean:
	rm -rf bin dist coverage.out
