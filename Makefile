VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X github.com/ofk0/piway/internal/version.Version=$(VERSION) \
           -X github.com/ofk0/piway/internal/version.Commit=$(COMMIT)

.PHONY: build build-go web web-lint web-test run test test-race lint fmt clean

# build: the dashboard first, then a binary that embeds it (internal/webui).
# build-go: the binary alone, serving the API only (no Node needed).
GOTAGS ?=

build: web
	$(MAKE) build-go GOTAGS=embedui

build-go:
	go build -tags "$(GOTAGS)" -ldflags "$(LDFLAGS)" -o bin/piway ./cmd/piway

# npm ci only reruns when the lockfile changes.
web/node_modules/.package-lock.json: web/package-lock.json
	cd web && npm ci --no-fund --no-audit

web: web/node_modules/.package-lock.json
	cd web && npm run build

web-lint: web/node_modules/.package-lock.json
	cd web && npm run lint

web-test: web/node_modules/.package-lock.json
	cd web && npm test

run: build
	./bin/piway

test:
	go test ./...

# The race detector needs a 48-bit VMA kernel; Raspberry Pi OS kernels use 39-bit, so run this in CI.
test-race:
	go test -race ./...

lint:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "gofmt needed" && exit 1)
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -rf bin internal/webui/dist
