VERSION ?= dev
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS = -s -w \
  -X main.version=$(VERSION) \
  -X main.commit=$(COMMIT) \
  -X main.date=$(DATE)

.PHONY: build install test fmt vet tidy clean snapshot release-check

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o ps3 ./cmd/ps3

install: build
	install -m 0755 ps3 $(or $(PREFIX),/usr/local/bin)/ps3

test:
	go test ./...

fmt:
	go fmt ./...

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf ps3 dist/

snapshot:
	goreleaser release --snapshot --clean

release-check:
	goreleaser check
