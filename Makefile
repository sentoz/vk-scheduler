SHELL := /bin/bash
.SHELLFLAGS := -Eeuo pipefail -c
RELEASE_MATRIX := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
BINARY := vk-scheduler
PKG := ./cmd/vk-scheduler
NATIVE_EXTENSION := $(if $(filter windows,$(shell go env GOOS)),.exe)
NATIVE_BINARY := build/$(BINARY)$(NATIVE_EXTENSION)
MODULE := $(shell go list -m)
VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null || printf dev)
COMMIT ?= $(shell git rev-parse --verify HEAD 2>/dev/null || printf unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
URL := https://$(MODULE)
LDFLAGS := -s -w -X $(MODULE)/internal/vars.Version=$(VERSION) -X $(MODULE)/internal/vars.Commit=$(COMMIT) -X $(MODULE)/internal/vars._buildTime=$(DATE) -X $(MODULE)/internal/vars.URL=$(URL)
TOOLBIN ?= $(or $(shell go env GOBIN),$(shell go env GOPATH)/bin)
LINTER ?= $(TOOLBIN)/golangci-lint
ALIGNER ?= $(TOOLBIN)/betteralign
CYCLONEDX ?= $(TOOLBIN)/cyclonedx-gomod

.PHONY: all init clean fmt schema-gen check-schemas vet lint align align-fix \
	test cover race check build release release-notes sbom sbom-app sbom-bin \
	container kustomize tools
all: check build
init: tools
	go mod download
clean:
	rm -rf -- ./build
	rm -f -- ./coverage.out
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
	"$(LINTER)" run ./...
align:
	"$(ALIGNER)" ./...
# A field type's new layout can expose a second fix in the struct that uses it.
align-fix:
	@for pass in 1 2 3 4 5; do \
		status=0; \
		"$(ALIGNER)" -apply ./... || status=$$?; \
		if [[ "$$status" -ne 3 ]]; then exit "$$status"; fi; \
	done; \
	"$(ALIGNER)" ./...
test:
	go test ./...
cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out
race:
	CGO_ENABLED=1 go test -race ./...
check: check-schemas vet lint test race
	@test -z "$$(gofmt -l cmd internal integration static)"
build:
	@mkdir -p build
	CGO_ENABLED=0 go build -trimpath -ldflags='$(LDFLAGS)' -o "$(NATIVE_BINARY)" $(PKG)
release: sbom-app
	@mkdir -p build
	@for target in $(RELEASE_MATRIX); do \
		goos=$${target%%/*}; goarch=$${target##*/}; ext=""; \
		if [[ "$$goos" == windows ]]; then ext=.exe; fi; \
		out="build/$(BINARY)-$$goos-$$goarch$$ext"; \
		printf 'Building %s\n' "$$out"; \
		CGO_ENABLED=0 GOOS="$$goos" GOARCH="$$goarch" go build -trimpath -ldflags='$(LDFLAGS)' -o "$$out" $(PKG); \
	done
release-notes:
	@awk '\
		/^## \[[0-9]+\.[0-9]+\.[0-9]+([-+][^]]+)?\]/ && !found { found = 1; next } \
		found && (/^## / || /^<!--links-->/ || /^\[[^]]+\]:/) { exit } \
		found { print } \
		END { if (!found) { print "No versioned release in CHANGELOG.md" > "/dev/stderr"; exit 1 } } \
	' CHANGELOG.md
sbom: sbom-app sbom-bin
sbom-app:
	@mkdir -p build
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$(CYCLONEDX)" app -json -packages -files -licenses \
		-output "build/$(BINARY).app.sbom.json" -main "$(PKG)"
sbom-bin: build
	"$(CYCLONEDX)" bin -json -output "$(NATIVE_BINARY).sbom.json" "$(NATIVE_BINARY)"
container:
	docker build -t vk-scheduler:local .
kustomize:
	kubectl kustomize deploy/
tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
	go install github.com/dkorunic/betteralign/cmd/betteralign@v0.9.1
	go install github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@v1.10.0
