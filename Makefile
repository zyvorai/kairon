SHELL := /bin/sh
GO ?= go
IMAGE ?= ghcr.io/zyvorai/kairon
TAG ?= dev

.PHONY: all fmt vet test test-race build clean validate docker-build deploy-remote
all: fmt vet test build validate

fmt:
	gofmt -w $$(find cmd internal -name '*.go' -type f)

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/kairon-controller ./cmd/kairon-controller
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/kairon-node ./cmd/kairon-node
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/kaironctl ./cmd/kaironctl

validate:
	python3 scripts/validate.py

docker-build:
	docker build --target controller -t $(IMAGE)-controller:$(TAG) .
	docker build --target node -t $(IMAGE)-node:$(TAG) .

deploy-remote:
	./scripts/deploy-remote.sh $(ARGS)

dist: all
	mkdir -p dist
	tar --exclude='./dist' --exclude='./bin' -czf dist/kairon-src.tar.gz .

clean:
	rm -rf bin dist coverage.out
