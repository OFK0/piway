VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X github.com/ofk0/piway/internal/version.Version=$(VERSION) \
           -X github.com/ofk0/piway/internal/version.Commit=$(COMMIT)

.PHONY: build build-go dist notices web web-lint web-test run test test-race lint fmt clean pi install deploy-pi check-pi

# build: the dashboard first, then a binary that embeds it (internal/webui).
# build-go: the binary alone, serving the API only (no Node needed).
GOTAGS ?=

build: notices
	$(MAKE) build-go GOTAGS=embedui

build-go:
	go build -tags "$(GOTAGS)" -ldflags "$(LDFLAGS)" -o bin/piway ./cmd/piway

# notices: THIRD_PARTY_NOTICES.txt, the licenses of everything the binary
# contains (Go standard library and modules, then the dashboard's npm
# packages and fonts). It is embedded with the dashboard and served at
# /THIRD_PARTY_NOTICES.txt, and it ships in the release archive.
WEBUI_DIST := internal/webui/dist
NOTICES    := $(WEBUI_DIST)/THIRD_PARTY_NOTICES.txt

notices: web
	{ echo "piway includes third-party software under the licenses below."; \
	  echo "piway itself is licensed under the MIT license (LICENSE)."; echo; \
	  scripts/go-notices.sh; echo; \
	  cat $(WEBUI_DIST)/THIRD_PARTY_NOTICES.web.txt; } > $(NOTICES).tmp
	rm $(WEBUI_DIST)/THIRD_PARTY_NOTICES.web.txt
	mv $(NOTICES).tmp $(NOTICES)

# dist: the release archive for the Pi, plus its checksum (docs/RELEASING.md).
# The archive is reproducible: the same commit always gives the same bytes,
# so anyone can rebuild it and compare the checksum. Timestamps come from
# the commit, owners are zeroed, files are sorted, and gzip stores no name
# or time; -trimpath keeps the build machine's paths out of the binary.
DIST       := dist
DIST_NAME  := piway_linux_arm64
SOURCE_DATE_EPOCH ?= $(shell git log -1 --format=%ct 2>/dev/null || echo 0)

dist: notices
	rm -rf $(DIST) && mkdir -p $(DIST)/$(DIST_NAME)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -tags embedui \
		-ldflags "$(LDFLAGS)" -o $(DIST)/$(DIST_NAME)/piway ./cmd/piway
	cp LICENSE $(NOTICES) README.md deploy/systemd/piway.service deploy/config.example.yaml $(DIST)/$(DIST_NAME)/
	tar --sort=name --mtime=@$(SOURCE_DATE_EPOCH) --owner=0 --group=0 --numeric-owner --mode=go-w \
		-C $(DIST) -cf - $(DIST_NAME) | gzip -9n > $(DIST)/$(DIST_NAME).tar.gz
	rm -rf $(DIST)/$(DIST_NAME)
	cd $(DIST) && sha256sum $(DIST_NAME).tar.gz > checksums.txt
	@cat $(DIST)/checksums.txt

# The development loop (docs/DEVELOPMENT.md).
# pi: a binary for the Pi from any machine: cross-compiled, dashboard included.
# install: on the Pi, build and swap in this checkout's binary, then restart.
# deploy-pi: from another machine, the same over ssh: make deploy-pi PI=pi@raspberrypi.local
# Both only replace the binary; run `piway setup` once first (docs/INSTALL.md).
PI      ?=
PI_ADDR ?= 127.0.0.1:8080
PI_BIN  := bin/piway-linux-arm64

pi: notices
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -tags embedui -ldflags "$(LDFLAGS)" -o $(PI_BIN) ./cmd/piway

install: build
	cp bin/piway $(HOME)/.local/bin/piway.new
	scripts/swap-binary.sh $(HOME)/.local/bin/piway.new $(PI_ADDR)

check-pi:
	@test -n "$(PI)" || { echo "set PI to the Pi's ssh destination: make deploy-pi PI=pi@raspberrypi.local"; exit 2; }

deploy-pi: check-pi pi
	ssh $(PI) 'mkdir -p .local/bin'
	scp -q $(PI_BIN) $(PI):.local/bin/piway.new
	ssh $(PI) 'sh -s -- .local/bin/piway.new $(PI_ADDR)' < scripts/swap-binary.sh

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
	rm -rf bin dist internal/webui/dist
