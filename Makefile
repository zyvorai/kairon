# Copyright 2026 Zyvor · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0

SHELL := /bin/sh
GO ?= go
IMAGE ?= ghcr.io/zyvorai/kairon
TAG ?= dev
VERSION ?= $(shell cat VERSION)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all fmt fmt-check vet test test-race build clean validate docker-build smoke
all: fmt-check vet test build validate smoke

fmt:
	gofmt -w $$(find cmd internal -name '*.go' -type f)

fmt-check:
	test -z "$$(gofmt -l cmd internal)"

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/kairon-controller ./cmd/kairon-controller
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/kairon-node ./cmd/kairon-node
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/kaironctl ./cmd/kaironctl

smoke: build
	test "$$($(CURDIR)/bin/kaironctl version)" = "$(VERSION)"
	test "$$($(CURDIR)/bin/kairon-controller --version)" = "$(VERSION)"
	test "$$($(CURDIR)/bin/kairon-node --version)" = "$(VERSION)"

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
