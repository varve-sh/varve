.PHONY: build test lint clean install snapshot release

BINARY   = varve
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS  = -ldflags "-s -w -X main.version=$(VERSION)"

build:
	CGO_ENABLED=0 go build $(LDFLAGS) -o bin/$(BINARY) ./cmd/varve

test:
	VARVE_EMBED_PROVIDER=disabled go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -rf bin/ dist/

install: build
	@mkdir -p $(shell go env GOPATH)/bin
	cp bin/$(BINARY) $(shell go env GOPATH)/bin/$(BINARY)
	@command -v $(BINARY) >/dev/null 2>&1 || printf '\ninstalled to %s/bin/%s, but that directory is not on your PATH.\nadd it:  export PATH="$$PATH:%s/bin"\n\n' "$(shell go env GOPATH)" "$(BINARY)" "$(shell go env GOPATH)"

tidy:
	go mod tidy

# Build all platforms locally without publishing (requires goreleaser)
snapshot:
	goreleaser release --snapshot --clean

# Tag and push a release — triggers the release workflow
# Usage: make release VERSION=1.2.3
release:
	@test -n "$(VERSION)" || (echo "usage: make release VERSION=x.y.z" && exit 1)
	git tag -a v$(VERSION) -m "Release v$(VERSION)"
	git push origin v$(VERSION)
