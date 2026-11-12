SHELL := /bin/bash
.SHELLFLAGS := -Eeuo pipefail -c
RELEASE_MATRIX := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
BINARY := vk-scheduler
PKG := ./cmd/vk-scheduler
MODULE := $(shell go list -m)
VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null || printf dev)
COMMIT ?= $(shell git rev-parse --verify HEAD 2>/dev/null || printf unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
URL := https://$(MODULE)
LDFLAGS := -s -w -X $(MODULE)/internal/vars.Version=$(VERSION) -X $(MODULE)/internal/vars.Commit=$(COMMIT) -X $(MODULE)/internal/vars._buildTime=$(DATE) -X $(MODULE)/internal/vars.URL=$(URL)
LINTER ?= golangci-lint

.PHONY: all fmt schema-gen check-schemas vet lint test race check build release container kustomize tools
all: check build
fmt:
	gofmt -w cmd internal integration static
schema-gen:
	go run ./cmd/schema-gen -out static/schemas/config.json -schedule-out static/schemas/schedule.json
check-schemas:
	@dir=$$(mktemp -d); trap 'rm -rf "$$dir"' EXIT; \
	go run ./cmd/schema-gen -out "$$dir/config.json" -schedule-out "$$dir/schedule.json"; \
	cmp static/schemas/config.json "$$dir/config.json"; \
	cmp static/schemas/schedule.json "$$dir/schedule.json"
vet:
	go vet ./...
lint:
	$(LINTER) run ./...
test:
	go test ./...
race:
	CGO_ENABLED=1 go test -race ./...
check: check-schemas vet lint test race
	@test -z "$$(gofmt -l cmd internal integration static)"
build:
	@mkdir -p build
	CGO_ENABLED=0 go build -trimpath -ldflags='$(LDFLAGS)' -o build/$(BINARY) $(PKG)
release:
	@mkdir -p build
	@for target in $(RELEASE_MATRIX); do \
		goos=$${target%%/*}; goarch=$${target##*/}; ext=""; \
		if [[ "$$goos" == windows ]]; then ext=.exe; fi; \
		out="build/$(BINARY)-$$goos-$$goarch$$ext"; \
		printf 'Building %s\n' "$$out"; \
		CGO_ENABLED=0 GOOS="$$goos" GOARCH="$$goarch" go build -trimpath -ldflags='$(LDFLAGS)' -o "$$out" $(PKG); \
	done
container:
	docker build -t vk-scheduler:local .
kustomize:
	kubectl kustomize deploy/
tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
