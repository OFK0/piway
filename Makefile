VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X github.com/ofk0/piway/internal/version.Version=$(VERSION) \
           -X github.com/ofk0/piway/internal/version.Commit=$(COMMIT)

.PHONY: build run test test-race lint fmt clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/piway ./cmd/piway

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
	rm -rf bin
